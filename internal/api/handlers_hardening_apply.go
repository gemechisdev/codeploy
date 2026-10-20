// Package api: P2b hardening apply path. Plan and preflight are read-only; apply/confirm/revert are the only
// routes that mutate the host. Every host mutation snapshots first, and every state change lands together with
// its audit row in the same transaction (bus events are published only after commit).
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/hardening"
	"github.com/codeplo/codeplo/internal/platform"
	"github.com/codeplo/codeplo/internal/store"
)

// Seams for the apply path. Production points at the real host; tests repoint them at a temp tree and a stub
// runner so unit tests never exec systemctl/apt/nft or write a real /etc.
var (
	hardeningRoot                      = "/"
	hardeningRevertDir                 = hardening.DefaultRevertDir
	hardeningRun       platform.Runner = platform.DefaultRunner
)

// nftRulesetPath is where the rendered Tier 2 ruleset is written before plan.go's `nft -f` Post command loads
// it; it must stay in sync with the Post command Planner.Plan builds for fw.default_deny.
const nftRulesetPath = "/var/lib/codeplo/plan/codeplo.nft"

func newHardeningPlanner() *hardening.Planner {
	return &hardening.Planner{Env: hardening.Env{Root: hardeningRoot}, Run: hardeningRun}
}

// hardeningCheckInfo runs the read-only detectors and indexes them by key (source of tier and human name).
func hardeningCheckInfo(flags map[string]bool) map[string]hardening.Check {
	cs := hardening.Run(hardening.Env{Root: hardeningRoot}, flags)
	m := make(map[string]hardening.Check, len(cs))
	for _, c := range cs {
		m[c.Key] = c
	}
	return m
}

func hardeningCheckName(key string) string {
	for _, c := range hardening.Run(hardening.Env{Root: hardeningRoot}, nil) {
		if c.Key == key {
			return c.Name
		}
	}
	return key
}

// hardeningParams resolves the per-request planning inputs: the operator account (setup_state first, else the
// first sudo candidate that already has keys) and the admin source IPs that must never be blocked or banned.
func (s *Server) hardeningParams(ctx context.Context, r *http.Request) hardening.Params {
	prm := hardening.Params{AdminIPs: hardening.AdminIPs(ctx, hardeningRun, clientIP(r))}
	var op sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT operator FROM setup_state WHERE id=1`).Scan(&op)
	prm.Operator = op.String
	if prm.Operator == "" {
		for _, c := range platform.OperatorCandidates() {
			if c.Sudo && c.HasAuthorizedKeys {
				prm.Operator = c.Username
				break
			}
		}
	}
	return prm
}

// hardeningPlan is read-only: it renders change plans (Tier 3 included, even while apply is flag-gated off —
// plan-only is the default posture) and records a mode='plan' run with the current detector results.
func (s *Server) hardeningPlan(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	p := from(r)
	ctx := r.Context()
	var b struct {
		CheckKeys []string `json:"check_keys"`
	}
	if !decode(w, r, &b) {
		return
	}
	if len(b.CheckKeys) == 0 {
		invalid(w, map[string]string{"check_keys": "Select at least one check"})
		return
	}
	planner := newHardeningPlanner()
	cs := hardening.Run(planner.Env, s.applyFlags(r))
	info := make(map[string]hardening.Check, len(cs))
	for _, c := range cs {
		info[c.Key] = c
	}
	prm := s.hardeningParams(ctx, r)
	items := []map[string]any{}
	results := []hardening.Check{}
	seen := map[string]bool{}
	for _, key := range b.CheckKeys {
		if seen[key] {
			continue
		}
		seen[key] = true
		c, ok := info[key]
		if !ok {
			invalid(w, map[string]string{"check_keys": `Unknown check "` + key + `"`})
			return
		}
		pln, err := planner.Plan(ctx, key, prm)
		if errors.Is(err, hardening.ErrNotApplicable) {
			invalid(w, map[string]string{"check_keys": key + " is report-only and has no apply action"})
			return
		} else if errors.Is(err, hardening.ErrNeedsParam) {
			invalid(w, map[string]string{"check_keys": key + ": " + err.Error()})
			return
		} else if err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, map[string]any{"check_key": key, "plan_diff": pln.Diff, "commands": pln.Commands()})
		results = append(results, c)
	}
	runID, started, finished := newID("hrun"), now(), now()
	score := hardening.Score(cs)
	err := store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hardening_runs(id,mode,started_at,finished_at,actor_id,score) VALUES (?,?,?,?,?,?)`, runID, "plan", started, finished, p.User.ID, score); err != nil {
			return err
		}
		for _, c := range results {
			if _, err := tx.Exec(`INSERT INTO hardening_results(run_id,check_key,tier,status,detail) VALUES (?,?,?,?,?)`, runID, c.Key, c.Tier, string(c.Status), c.DetectDetail); err != nil {
				return err
			}
		}
		_, err := audit.Append(ctx, tx, audit.Entry{ActorType: "user", ActorID: p.User.ID, ActorEmail: p.User.Email, IP: clientIP(r),
			Action: "hardening.plan", TargetType: "hardening_run", TargetID: runID, Meta: map[string]any{"check_keys": b.CheckKeys, "score": score}})
		return err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("audit.appended", map[string]any{"action": "hardening.plan"})
	s.Bus.Publish("hardening.changed", map[string]any{"run_id": runID})
	writeJSON(w, 200, map[string]any{"items": items})
}

// hardeningPreflight evaluates the blocking preconditions for one check; read-only, no run recorded.
func (s *Server) hardeningPreflight(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	ctx := r.Context()
	var b struct {
		CheckKey string `json:"check_key"`
	}
	if !decode(w, r, &b) {
		return
	}
	planner := newHardeningPlanner()
	info := hardeningCheckInfo(s.applyFlags(r))
	c, ok := info[b.CheckKey]
	if !ok {
		invalid(w, map[string]string{"check_key": `Unknown check "` + b.CheckKey + `"`})
		return
	}
	if c.Tier == 0 {
		writeErr(w, 409, "conflict", "Tier 0 checks are report-only and need no preflight", nil)
		return
	}
	prm := s.hardeningParams(ctx, r)
	items, err := planner.Preflight(ctx, b.CheckKey, prm.Operator, prm.AdminIPs)
	if err != nil {
		internalErr(w, err)
		return
	}
	if items == nil {
		items = []hardening.PreflightItem{}
	}
	writeJSON(w, 200, map[string]any{"check_key": b.CheckKey, "ok": hardening.OK(items), "items": items})
}

// hardeningApply is the only route that changes the host. Refusals all land before step-up so a doomed
// request never opens the 2FA dialog (spec x-step-up note).
func (s *Server) hardeningApply(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	p := from(r)
	ctx := r.Context()
	var b struct {
		CheckKey string `json:"check_key"`
		Ack      *struct {
			ConsoleAccess bool `json:"console_access"`
		} `json:"ack"`
	}
	if !decode(w, r, &b) {
		return
	}
	flags := s.applyFlags(r)
	info := hardeningCheckInfo(flags)
	c, ok := info[b.CheckKey]
	if !ok {
		invalid(w, map[string]string{"check_key": `Unknown check "` + b.CheckKey + `"`})
		return
	}
	if c.Tier == 0 {
		writeErr(w, 409, "conflict", "Tier 0 checks are read-only and cannot be applied", nil)
		return
	}
	if gate := hardeningApplyGate(c.Tier, flags); gate != "" {
		writeErr(w, 409, "conflict", `Applying this check is plan-only: feature flag "`+gate+`" is off`, map[string]any{"flag": gate, "tier": c.Tier})
		return
	}
	if c.Tier == 1 && c.Status == hardening.Pass {
		// Checks are idempotent (plan P2 business rules): a satisfied Tier 1 check has nothing to do.
		writeErr(w, 409, "conflict", "Nothing to apply: this check already passes", map[string]any{"check_key": b.CheckKey, "status": string(c.Status)})
		return
	}
	if c.Tier == 3 && (b.Ack == nil || !b.Ack.ConsoleAccess) {
		invalid(w, map[string]string{"ack": "Confirm that you have provider console or rescue access before applying"})
		return
	}
	var activeID, activeState string
	err := s.DB.QueryRowContext(ctx, `SELECT id, state FROM hardening_changes WHERE check_key=? AND state IN ('pending_confirm','confirmed','reverting') ORDER BY applied_at DESC LIMIT 1`, b.CheckKey).
		Scan(&activeID, &activeState)
	if err == nil {
		writeErr(w, 409, "conflict", "This check is already applied; confirm or revert the existing change first", map[string]any{"change_id": activeID, "state": activeState})
		return
	}
	if err != sql.ErrNoRows {
		internalErr(w, err)
		return
	}
	if c.Tier >= 2 {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM hardening_changes WHERE state='pending_confirm'`).Scan(&n); err != nil {
			internalErr(w, err)
			return
		}
		if n > 0 {
			writeErr(w, 409, "conflict", "Another change is waiting for confirmation. Confirm or revert it first.", nil)
			return
		}
	}
	planner := newHardeningPlanner()
	prm := s.hardeningParams(ctx, r)
	pln, err := planner.Plan(ctx, b.CheckKey, prm)
	if errors.Is(err, hardening.ErrNeedsParam) {
		invalid(w, map[string]string{"check_key": err.Error()})
		return
	} else if err != nil {
		internalErr(w, err) // tier-checked above: anything else the planner refuses here is a server fault
		return
	}
	items, err := planner.Preflight(ctx, b.CheckKey, prm.Operator, prm.AdminIPs)
	if err != nil {
		internalErr(w, err)
		return
	}
	acked := c.Tier == 3 && b.Ack != nil && b.Ack.ConsoleAccess
	if !hardeningPreflightPass(items, acked) {
		writeErr(w, 409, "preflight_failed", "Pre-flight checks failed. Nothing was changed.", map[string]any{"checks": items})
		return
	}
	if !requireStepUp(w, p) {
		return
	}

	// Host order: WriteSnapshot first, then apply (restoring the snapshot on any failure).
	id := newID("hchg")
	snap, err := s.applyHardeningChange(ctx, planner, pln, id)
	if err != nil {
		// The engine restored the snapshot on failure; never leak command output.
		writeErr(w, 500, "internal", "Apply failed; the snapshot was restored where possible", nil)
		return
	}
	window := s.settingInt(ctx, "hardening_revert_window_s", 300)
	t0 := time.Now()
	appliedAt := iso(t0)
	guarded := c.Tier >= 2
	var unit, deadline string
	// Only Tier 2/3 can cut access, so only they arm the daemon-independent dead-man (plan 3.10). Tier 1 fixes
	// cannot lock the operator out: arming a timer for them would make every safe fix depend on systemd-run and
	// hold the change open for confirmation it does not need.
	if guarded {
		// The dead-man is armed before the change is recorded: if arming fails the host change is rolled back
		// rather than left live without a revert path. If the DB write then fails, the timer still fires and
		// reverts from the snapshot (fail-safe in the connectivity-protecting direction).
		unit, err = hardening.ScheduleRevertTimer(ctx, hardeningRun, hardeningBinary(), id, time.Duration(window)*time.Second)
		if err != nil {
			_ = hardening.Revert(hardeningRoot, hardeningRevertDir, id)
			_ = hardening.RuntimeRevert(ctx, hardeningRevertDir, id, hardeningRun)
			writeErr(w, 500, "internal", "The dead-man timer could not be armed; the change was rolled back", nil)
			return
		}
		deadline = iso(t0.Add(time.Duration(window) * time.Second))
	}
	state := "confirmed"
	if guarded {
		state = "pending_confirm"
	}
	runID, started, finished := newID("hrun"), now(), now()
	score := hardening.Score(hardening.Run(planner.Env, flags))
	paramsJSON, _ := json.Marshal(map[string]any{"admin_ips": prm.AdminIPs, "operator": prm.Operator, "console_access": acked})
	change := hardeningChangeRow{ID: id, CheckKey: b.CheckKey, Tier: c.Tier, SnapshotPath: snap, AppliedAt: appliedAt, State: state}
	change.RevertUnit = sql.NullString{String: unit, Valid: unit != ""}
	change.ConfirmDeadline = sql.NullString{String: deadline, Valid: deadline != ""}
	if !guarded {
		change.ConfirmedAt = sql.NullString{String: appliedAt, Valid: true}
	}
	err = store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hardening_runs(id,mode,started_at,finished_at,actor_id,score) VALUES (?,?,?,?,?,?)`, runID, "apply", started, finished, p.User.ID, score); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO hardening_changes(id,run_id,check_key,tier,snapshot_path,revert_unit,params,applied_at,confirm_deadline,confirmed_at,state) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			id, runID, b.CheckKey, c.Tier, snap, nullStr(unit), string(paramsJSON), appliedAt, nullStr(deadline), nullStr(change.ConfirmedAt.String), state); err != nil {
			return err
		}
		_, err := audit.Append(ctx, tx, audit.Entry{ActorType: "user", ActorID: p.User.ID, ActorEmail: p.User.Email, IP: clientIP(r),
			Action: "hardening.applied", TargetType: "hardening_change", TargetID: id,
			Meta: map[string]any{"check_key": b.CheckKey, "tier": c.Tier, "console_access_ack": acked, "guarded": guarded, "run_id": runID}})
		return err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("audit.appended", map[string]any{"action": "hardening.applied"})
	s.Bus.Publish("hardening.changed", map[string]any{"change_id": id})
	writeJSON(w, 200, hardeningChangeJSON(change, c.Name))
}

// hardeningConfirm records that the operator still has access. Runtime-first semantics (plan 3.10.2): the ruleset
// is already live in the kernel from apply time; confirming persists it for boot, then cancels the dead-man and
// makes the row permanent. That order is the safe one: if the boot files cannot be written the timer stays armed,
// so a change that would vanish at reboot never gets marked permanent.
func (s *Server) hardeningConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	p := from(r)
	ctx := r.Context()
	id := chi.URLParam(r, "changeId")
	ch, err := s.loadHardeningChange(ctx, id)
	if err == sql.ErrNoRows {
		writeErr(w, 404, "not_found", "change not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	if ch.State != "pending_confirm" {
		writeErr(w, 409, "conflict", "Change is "+ch.State+"; only a pending change can be confirmed", map[string]any{"state": ch.State})
		return
	}
	if err := hardeningPersistBoot(ctx, newHardeningPlanner(), ch); err != nil {
		writeErr(w, 500, "internal", "The change could not be persisted for boot; it stays guarded and will revert on its own unless confirmed again", nil)
		return
	}
	if ch.RevertUnit.Valid && ch.RevertUnit.String != "" {
		// Disarm the dead-man before recording the confirmation: confirming while the timer still exists would
		// let it auto-revert a change the operator just made permanent.
		if _, err := hardeningRun(ctx, "systemctl", "stop", ch.RevertUnit.String+".timer"); err != nil {
			internalErr(w, err)
			return
		}
	}
	confirmedAt := now()
	err = store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if err := updateHardeningChange(ctx, tx, id, `UPDATE hardening_changes SET state='confirmed', confirmed_at=? WHERE id=? AND state='pending_confirm'`, confirmedAt); err != nil {
			return err
		}
		_, err := audit.Append(ctx, tx, audit.Entry{ActorType: "user", ActorID: p.User.ID, ActorEmail: p.User.Email, IP: clientIP(r),
			Action: "hardening.confirmed", TargetType: "hardening_change", TargetID: id, Meta: map[string]any{"check_key": ch.CheckKey}})
		return err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("audit.appended", map[string]any{"action": "hardening.confirmed"})
	s.Bus.Publish("hardening.changed", map[string]any{"change_id": id})
	ch.State, ch.ConfirmedAt = "confirmed", sql.NullString{String: confirmedAt, Valid: true}
	writeJSON(w, 200, hardeningChangeJSON(ch, hardeningCheckName(ch.CheckKey)))
}

// hardeningRevert restores the snapshot now. It works for pending_confirm and confirmed rows: an operator who
// confirmed too early must still be able to roll the change back.
func (s *Server) hardeningRevert(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	p := from(r)
	ctx := r.Context()
	id := chi.URLParam(r, "changeId")
	ch, err := s.loadHardeningChange(ctx, id)
	if err == sql.ErrNoRows {
		writeErr(w, 404, "not_found", "change not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	if ch.State != "pending_confirm" && ch.State != "confirmed" {
		writeErr(w, 409, "conflict", "Change is "+ch.State+"; only a pending or confirmed change can be reverted", map[string]any{"state": ch.State})
		return
	}
	if ch.RevertUnit.Valid && ch.RevertUnit.String != "" {
		_, _ = hardeningRun(ctx, "systemctl", "stop", ch.RevertUnit.String+".timer") // best effort: a missed cancel re-runs the idempotent snapshot restore
	}
	if err := hardening.Revert(hardeningRoot, hardeningRevertDir, id); err != nil {
		slog.Error("hardening snapshot restore failed", "change_id", id, "err", err)
		internalErr(w, err) // state is untouched, so the revert can be retried
		return
	}
	// Restoring files alone does not undo a runtime-first change: the live table inet codeplo (or the running
	// sshd configuration) comes down through the recorded steps, so a reverted firewall is actually off.
	if err := hardening.RuntimeRevert(ctx, hardeningRevertDir, id, hardeningRun); err != nil {
		slog.Error("hardening live-state revert failed", "change_id", id, "err", err)
		internalErr(w, err) // both halves are idempotent, so the retry is safe
		return
	}
	revertedAt := now()
	err = store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if err := updateHardeningChange(ctx, tx, id, `UPDATE hardening_changes SET state='reverted', reverted_at=? WHERE id=? AND state IN ('pending_confirm','confirmed')`, revertedAt); err != nil {
			return err
		}
		_, err := audit.Append(ctx, tx, audit.Entry{ActorType: "user", ActorID: p.User.ID, ActorEmail: p.User.Email, IP: clientIP(r),
			Action: "hardening.reverted", TargetType: "hardening_change", TargetID: id, Meta: map[string]any{"check_key": ch.CheckKey}})
		return err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("audit.appended", map[string]any{"action": "hardening.reverted"})
	s.Bus.Publish("hardening.changed", map[string]any{"change_id": id})
	ch.State, ch.RevertedAt = "reverted", sql.NullString{String: revertedAt, Valid: true}
	writeJSON(w, 200, hardeningChangeJSON(ch, hardeningCheckName(ch.CheckKey)))
}

// ---- apply-path helpers ----

// hardeningApplyGate returns the feature flag that must be ON before this tier may touch the host ("" when
// the tier is not gated: Tier 1 is always applyable).
func hardeningApplyGate(tier int, flags map[string]bool) string {
	switch {
	case tier == 2 && !flags["hardening.apply_tier2"]:
		return "hardening.apply_tier2"
	case tier == 3 && !flags["hardening.apply_tier3"]:
		return "hardening.apply_tier3"
	}
	return ""
}

// hardeningPreflightPass gates apply on the engine preflight. Every item must be OK except console_ack,
// which is the operator's own acknowledgement: preflight.go always reports it failing until apply records
// the ack, so a present ack satisfies it here (preflight endpoint output itself stays raw).
func hardeningPreflightPass(items []hardening.PreflightItem, acked bool) bool {
	for _, i := range items {
		if i.Key == "console_ack" && acked {
			continue
		}
		if !i.OK {
			return false
		}
	}
	return true
}

// applyHardeningChange snapshots, then mutates the host. Tier 1 uses the engine's Apply (which re-snapshots
// and restores on any command failure). Tier 2/3 are runtime-first here because Planner.Apply rejects those
// tiers until their release gates pass: it writes the rendered content, then runs only the plan's read-then-
// load Post commands. Any failure restores the snapshot before the error is returned.
func (s *Server) applyHardeningChange(ctx context.Context, planner *hardening.Planner, pln hardening.Plan, id string) (string, error) {
	var paths []string
	for _, f := range pln.Files {
		paths = append(paths, f.Path)
	}
	if pln.Tier == 3 && pln.Diff != nil && strings.HasPrefix(pln.Diff.Target, "/") {
		paths = append(paths, pln.Diff.Target) // the ssh drop-in lives in Diff, not Files
	}
	snap, err := hardening.WriteSnapshot(hardeningRoot, hardeningRevertDir, id, paths)
	if err != nil {
		return "", err
	}
	// The live-state restore is recorded before anything changes: a snapshot that cannot describe how to undo
	// the kernel state must stop the apply, not leave the dead-man restoring files while the ruleset stays live.
	if err := hardening.PlanRuntimeRevert(hardeningRevertDir, id, pln); err != nil {
		return "", err
	}
	if pln.Tier == 1 {
		return planner.Apply(ctx, pln, hardeningRevertDir, id)
	}
	fail := func(cause error) (string, error) {
		_ = hardening.Revert(hardeningRoot, hardeningRevertDir, id)
		_ = hardening.RuntimeRevert(ctx, hardeningRevertDir, id, hardeningRun)
		return snap, cause
	}
	switch pln.Tier {
	case 2:
		if pln.Diff == nil {
			return fail(errors.New("firewall plan carries no ruleset"))
		}
		if err := writeHardeningHostFile(hardeningRoot, nftRulesetPath, pln.Diff.After); err != nil {
			return fail(err)
		}
	case 3:
		if pln.Diff == nil || !strings.HasPrefix(pln.Diff.Target, "/") {
			return fail(errors.New("ssh plan carries no drop-in path"))
		}
		if err := writeHardeningHostFile(hardeningRoot, pln.Diff.Target, pln.Diff.After); err != nil {
			return fail(err)
		}
	default:
		return fail(errors.New("this tier cannot be applied in this build"))
	}
	for _, c := range pln.Post {
		if out, err := hardeningRun(ctx, c.Name, c.Args...); err != nil {
			return fail(fmt.Errorf("%s: %v: %s", c.String(), err, strings.TrimSpace(out)))
		}
	}
	return snap, nil
}

func writeHardeningHostFile(root, path, content string) error {
	real := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return err
	}
	return os.WriteFile(real, []byte(content), 0o644)
}

// hardeningPersistBoot writes the boot-time state for a confirmed change. Only the firewall needs it: every
// other check is a file the owning service already reads at boot, while the Tier 2 ruleset was loaded into the
// kernel at apply time and lives nowhere persistent yet. The include line is appended to the operator's own
// /etc/nftables.conf verbatim, and the unit is enabled but not reloaded, so the running ruleset is untouched.
func hardeningPersistBoot(ctx context.Context, planner *hardening.Planner, ch hardeningChangeRow) error {
	if ch.CheckKey != "fw.default_deny" {
		return nil
	}
	existing, err := os.ReadFile(filepath.Join(hardeningRoot, filepath.FromSlash(hardening.NftBootConfPath)))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, f := range hardening.NftablesBootFiles(planner.SSHPorts(ctx), string(existing)) {
		if err := writeHardeningHostFile(hardeningRoot, f.Path, f.Content); err != nil {
			return err
		}
	}
	for _, c := range hardening.NftablesBootCmds() {
		if out, err := hardeningRun(ctx, c.Name, c.Args...); err != nil {
			return fmt.Errorf("%s: %v: %s", c.String(), err, strings.TrimSpace(out))
		}
	}
	return nil
}

// hardeningBinary is the absolute path of this executable: the transient timer runs `<bin> revert <id>`
// outside the daemon's environment, so a relative argv[0] would not resolve.
func hardeningBinary() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "codeplo"
}

// ---- stored change rows ----

type hardeningChangeRow struct {
	ID, CheckKey, SnapshotPath, AppliedAt, State         string
	Tier                                                 int
	RevertUnit, ConfirmDeadline, ConfirmedAt, RevertedAt sql.NullString
}

func (s *Server) loadHardeningChange(ctx context.Context, id string) (hardeningChangeRow, error) {
	var c hardeningChangeRow
	err := s.DB.QueryRowContext(ctx, `SELECT id, check_key, tier, snapshot_path, revert_unit, applied_at, confirm_deadline, confirmed_at, reverted_at, state FROM hardening_changes WHERE id=?`, id).
		Scan(&c.ID, &c.CheckKey, &c.Tier, &c.SnapshotPath, &c.RevertUnit, &c.AppliedAt, &c.ConfirmDeadline, &c.ConfirmedAt, &c.RevertedAt, &c.State)
	return c, err
}

// updateHardeningChange runs a state-guarded UPDATE and refuses a silent no-op (a concurrent timer revert
// must not let an audit row land against a row that no longer has the expected state). Every statement here
// ends with `WHERE id=?`, so the change id is always the last placeholder.
func updateHardeningChange(ctx context.Context, tx *sql.Tx, id, stmt string, args ...any) error {
	res, err := tx.ExecContext(ctx, stmt, append(args, id)...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("change state changed concurrently: " + id)
	}
	return nil
}

// hardeningChangeJSON emits exactly the OpenAPI HardeningChange object (additionalProperties: false).
func hardeningChangeJSON(c hardeningChangeRow, name string) map[string]any {
	return map[string]any{"id": c.ID, "check_key": c.CheckKey, "check_name": name, "tier": c.Tier, "snapshot_path": c.SnapshotPath,
		"revert_unit": nullToAny(c.RevertUnit), "applied_at": c.AppliedAt, "confirm_deadline": nullToAny(c.ConfirmDeadline),
		"confirmed_at": nullToAny(c.ConfirmedAt), "reverted_at": nullToAny(c.RevertedAt), "state": c.State}
}

func nullToAny(n sql.NullString) any {
	if n.Valid {
		return n.String
	}
	return nil
}
