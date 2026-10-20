package api

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/hardening"
)

// The apply-path tests run the real router against a throwaway SQLite file (like api_test.go) with the host
// seams repointed: hardeningRoot at a temp tree and hardeningRun at a stub, so no real systemctl/apt/nft
// ever executes and no real /etc is touched on the machine running the tests.

type cmdCall struct {
	name string
	args []string
}

type stubRunner struct {
	mu      sync.Mutex
	calls   []cmdCall
	onCall  func(name string, args []string)
	failRun func(name string, args []string) bool
}

func (st *stubRunner) run(_ context.Context, name string, args ...string) (string, error) {
	st.mu.Lock()
	st.calls = append(st.calls, cmdCall{name: name, args: args})
	fail := st.failRun
	st.mu.Unlock()
	if st.onCall != nil {
		st.onCall(name, args)
	}
	// Tests that need the host to answer (a failing `nft list` probe, a broken systemd-run) set failRun; the
	// default stub pretends every command succeeds.
	if fail != nil && fail(name, args) {
		return "simulated host failure", errors.New("simulated failure")
	}
	return "", nil
}

func (st *stubRunner) has(name string) bool { return st.called(name) }

// indexOf returns the position of the first matching call, or -1. It proves ordering, which `called` cannot:
// persisting a ruleset after the dead-man was disarmed would leave the change unpersisted if confirm crashed.
func (st *stubRunner) indexOf(name string, want ...string) int {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, c := range st.calls {
		if c.name != name {
			continue
		}
		joined := strings.Join(c.args, " ")
		ok := true
		for _, w := range want {
			if !strings.Contains(joined, w) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func (st *stubRunner) called(name string, want ...string) bool {
	return st.indexOf(name, want...) >= 0
}

func mountHardeningHost(t *testing.T) (root, revertDir string, st *stubRunner) {
	t.Helper()
	root = t.TempDir()
	revertDir = filepath.Join(root, "var", "lib", "codeplo", "revert")
	st = &stubRunner{}
	oldRoot, oldRevert, oldRun := hardeningRoot, hardeningRevertDir, hardeningRun
	hardeningRoot, hardeningRevertDir, hardeningRun = root, revertDir, st.run
	t.Cleanup(func() { hardeningRoot, hardeningRevertDir, hardeningRun = oldRoot, oldRevert, oldRun })
	return root, revertDir, st
}

// newHardeningEnv bootstraps an owner (full permissions, TOTP enrolled) and pins the operator account so
// planning never depends on the host's /etc/passwd.
func newHardeningEnv(t *testing.T) (*env, *client, string) {
	t.Helper()
	e := newEnv(t)
	c, secret := e.bootstrap()
	if _, err := e.s.DB.Exec(`UPDATE setup_state SET operator='opsuser' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	return e, c, secret
}

func hardeningStepUp(t *testing.T, c *client, secret string) {
	t.Helper()
	if w, b := c.post("/auth/step-up", map[string]string{"code": codeAt(t, secret, 0)}); w.Code != 200 {
		t.Fatalf("step-up: %d %v", w.Code, b)
	}
}

func hardeningApplyBody(key string, ack bool) map[string]any {
	b := map[string]any{"check_key": key}
	if ack {
		b["ack"] = map[string]any{"console_access": true}
	}
	return b
}

func hardeningScalar(t *testing.T, e *env, query string, args ...any) any {
	t.Helper()
	var v any
	if err := e.s.DB.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("scan %s: %v", query, err)
	}
	return v
}

func hardeningCount(t *testing.T, e *env, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.s.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", query, err)
	}
	return n
}

func hardeningTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("time %q: %v", s, err)
	}
	return v
}

func hardeningFileCount(t *testing.T, root string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func hardeningPlanItems(t *testing.T, body map[string]any) map[string]map[string]any {
	t.Helper()
	raw, _ := body["items"].([]any)
	out := map[string]map[string]any{}
	for _, it := range raw {
		m, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("item is not an object: %v", it)
		}
		key, _ := m["check_key"].(string)
		out[key] = m
	}
	return out
}

func hardeningCommands(item map[string]any) []string {
	raw, _ := item["commands"].([]any)
	out := []string{}
	for _, c := range raw {
		if s, ok := c.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// TestHardeningChecksCarryPlanAndApplyEnabled pins the Security page's two predicates to the contract the mock
// implements (web/src/mock/handlers/hardening.ts view()): apply_enabled is derived from tier, status and the
// release flag, and a fixable check publishes its plan diff. CheckCard only renders "Plan & apply" when
// plan_diff is present, so a response without diffs makes the whole feature unreachable.
func TestHardeningChecksCarryPlanAndApplyEnabled(t *testing.T) {
	e, c, _ := newHardeningEnv(t)
	_, _, st := mountHardeningHost(t)

	w, body := c.do("GET", "/hardening/checks", nil, nil)
	if w.Code != 200 {
		t.Fatalf("checks: %d %v", w.Code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("no checks served: %v", body)
	}
	// A GET must not touch the host: no systemctl, no nft, no sshd.
	if len(st.calls) != 0 {
		t.Fatalf("GET /hardening/checks executed commands: %v", st.calls)
	}
	// Keys whose fix is a file or a ruleset: the diff is what the UI shows before apply.
	needDiff := map[string]bool{"upd.unattended": true, "kern.sysctl": true, "bf.protection": true,
		"fw.default_deny": true, "ssh.no_password": true, "ssh.no_root_password": true, "ssh.limit_auth": true}
	seen := map[string]bool{}
	for _, raw := range items {
		it, _ := raw.(map[string]any)
		key, _ := it["key"].(string)
		tier, _ := it["tier"].(float64)
		seen[key] = true
		status, _ := it["status"].(string)
		want := it["apply_enabled"] == true
		switch {
		case tier == 0:
			if want {
				t.Fatalf("%s: tier 0 is report-only but apply_enabled=true", key)
			}
			if it["plan_diff"] != nil {
				t.Fatalf("%s: tier 0 must carry no plan diff", key)
			}
		case tier == 1:
			// A Tier 1 check is offered exactly when it does not pass: the fix is idempotent and unguarded.
			if want != (status != "pass") {
				t.Fatalf("%s: apply_enabled=%v but status=%s", key, want, status)
			}
		default:
			// Tier 2/3 follow the release flag, which seeds off (the flags were not touched by this test).
			if want {
				t.Fatalf("%s: gated tier offered apply while its release flag is off", key)
			}
			if flag, _ := it["apply_flag"].(string); flag == "" {
				t.Fatalf("%s: tier %v check must name its gate flag", key, tier)
			}
		}
		if needDiff[key] && it["plan_diff"] == nil {
			t.Fatalf("%s: fixable check served no plan_diff (the UI cannot offer a fix)", key)
		}
		if pd, _ := it["plan_diff"].(map[string]any); pd != nil {
			if pd["target"] == nil || pd["target"] == "" || pd["after"] == nil || pd["after"] == "" {
				t.Fatalf("%s: half-filled plan_diff: %v", key, pd)
			}
		}
	}
	for key := range needDiff {
		if !seen[key] {
			t.Fatalf("check %s missing from /hardening/checks", key)
		}
	}
	// Enabling the Tier 2 release flag must flip exactly that check.
	if _, err := e.s.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key='hardening.apply_tier2'`); err != nil {
		t.Fatal(err)
	}
	_, body = c.do("GET", "/hardening/checks", nil, nil)
	for _, raw := range body["items"].([]any) {
		it, _ := raw.(map[string]any)
		if it["key"] == "fw.default_deny" && it["apply_enabled"] != true {
			t.Fatalf("tier 2 check still disabled with its flag on: %v", it)
		}
		if it["key"] == "ssh.no_password" && it["apply_enabled"] != false {
			t.Fatalf("tier 3 must stay disabled while apply_tier3 is off: %v", it)
		}
	}
	// After an audit the stored results are what the page reads, and the diffs survive the round trip.
	if w, b := c.post("/hardening/audit", nil); w.Code != 200 {
		t.Fatalf("audit: %d %v", w.Code, b)
	}
	_, body = c.do("GET", "/hardening/checks", nil, nil)
	if body["last_run_at"] == nil {
		t.Fatal("checks must report the audit timestamp once one exists")
	}
	for _, raw := range body["items"].([]any) {
		it, _ := raw.(map[string]any)
		if it["key"] == "kern.sysctl" && it["plan_diff"] == nil {
			t.Fatalf("stored audit lost the plan diff: %v", it)
		}
	}
}

func TestHardeningPlanIsReadOnlyAndGeneratesGatedTiers(t *testing.T) {
	e, c, _ := newHardeningEnv(t)
	root, _, st := mountHardeningHost(t)

	keys := []string{"upd.unattended", "kern.sysctl", "bf.protection", "acct.operator", "fw.default_deny",
		"ssh.no_password", "ssh.no_root_password", "ssh.limit_auth"}
	w, body := c.post("/hardening/plan", map[string]any{"check_keys": keys})
	if w.Code != 200 {
		t.Fatalf("plan: %d %v", w.Code, body)
	}
	items := hardeningPlanItems(t, body)
	if len(items) != len(keys) {
		t.Fatalf("want %d plan items, got %d", len(keys), len(items))
	}
	// Tier 3 plans are generated although apply is flag-gated off (plan-only is the default posture).
	ssh, ok := items["ssh.no_password"]
	if !ok {
		t.Fatal("ssh.no_password plan missing")
	}
	pd, _ := ssh["plan_diff"].(map[string]any)
	if pd == nil || pd["target"] != "/etc/ssh/sshd_config.d/00-codeplo.conf" {
		t.Fatalf("ssh drop-in diff wrong: %v", ssh["plan_diff"])
	}
	if fw := items["fw.default_deny"]; fw == nil || !strings.Contains(fw["plan_diff"].(map[string]any)["after"].(string), "table inet codeplo") {
		t.Fatal("firewall ruleset diff missing")
	} else if !strings.Contains(strings.Join(hardeningCommands(fw), "\n"), "nft -f /var/lib/codeplo/plan/codeplo.nft") {
		t.Fatalf("firewall plan commands: %v", fw["commands"])
	}
	if acct := items["acct.operator"]; acct["plan_diff"].(map[string]any)["after"] != "opsuser" {
		t.Fatalf("operator not planned from setup_state: %v", acct["plan_diff"])
	}

	// Zero mutations: no files anywhere under the host root, no mutating commands.
	if n := hardeningFileCount(t, root); n != 0 {
		t.Fatalf("plan wrote %d files under the host root", n)
	}
	for _, name := range []string{"apt-get", "sysctl", "systemctl", "systemd-run", "nft"} {
		if st.has(name) {
			t.Fatalf("plan executed %s", name)
		}
	}

	// The run and its results are recorded.
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_runs WHERE mode='plan'`); n != 1 {
		t.Fatalf("plan run rows = %d", n)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_results hr JOIN hardening_runs h ON h.id=hr.run_id WHERE h.mode='plan'`); n != int64(len(keys)) {
		t.Fatalf("plan result rows = %d, want %d", n, len(keys))
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM audit_events WHERE action='hardening.plan'`); n != 1 {
		t.Fatalf("plan audit rows = %d", n)
	}

	// Tier 0 has no plan; unknown keys and empty selection are validation failures.
	if w, _ := c.post("/hardening/plan", map[string]any{"check_keys": []string{"net.exposed_services"}}); w.Code != 422 {
		t.Fatalf("tier 0 plan must be 422, got %d", w.Code)
	}
	if w, _ := c.post("/hardening/plan", map[string]any{"check_keys": []string{"nope"}}); w.Code != 422 {
		t.Fatalf("unknown key must be 422, got %d", w.Code)
	}
	if w, _ := c.post("/hardening/plan", map[string]any{"check_keys": []string{}}); w.Code != 422 {
		t.Fatalf("empty check_keys must be 422, got %d", w.Code)
	}
}

func TestHardeningPreflight(t *testing.T) {
	e, c, _ := newHardeningEnv(t)
	mountHardeningHost(t)

	w, body := c.post("/hardening/preflight", map[string]any{"check_key": "fw.default_deny"})
	if w.Code != 200 || body["ok"] != true {
		t.Fatalf("tier 2 preflight: %d %v", w.Code, body)
	}
	if body["check_key"] != "fw.default_deny" {
		t.Fatalf("check_key echo: %v", body["check_key"])
	}
	if raw, _ := body["items"].([]any); len(raw) < 3 {
		t.Fatalf("tier 2 must report port/edge/table items, got %v", body["items"])
	}

	// Tier 3 on a bare fixture tree: key login is not proven and there is no Include line, so ok=false
	// with the failing items named (the console_ack refusal is the operator's ack, taken at apply time).
	w, body = c.post("/hardening/preflight", map[string]any{"check_key": "ssh.no_password"})
	if w.Code != 200 || body["ok"] != false {
		t.Fatalf("unproven tier 3 must be ok=false: %d %v", w.Code, body)
	}
	keys := map[string]bool{}
	for _, it := range body["items"].([]any) {
		m := it.(map[string]any)
		if _, bad := m["label"]; !bad {
			t.Fatalf("preflight item missing label: %v", m)
		}
		if _, bad := m["detail"]; !bad {
			t.Fatalf("preflight item missing detail: %v", m)
		}
		keys[m["key"].(string)] = true
	}
	for _, want := range []string{"include", "sshd_t", "authorized_keys", "pubkey_evidence", "console_ack"} {
		if !keys[want] {
			t.Fatalf("preflight items missing %s: %v", want, keys)
		}
	}

	if w, b := c.post("/hardening/preflight", map[string]any{"check_key": "net.exposed_services"}); w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("tier 0 preflight must be 409 conflict, got %d %v", w.Code, b)
	}
	if w, _ := c.post("/hardening/preflight", map[string]any{"check_key": "nope"}); w.Code != 422 {
		t.Fatalf("unknown key must be 422, got %d", w.Code)
	}
	// Preflight is read-only: nothing is recorded.
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_runs`); n != 0 {
		t.Fatalf("preflight recorded %d runs", n)
	}
}

func TestHardeningApplyTierGating(t *testing.T) {
	e, c, _ := newHardeningEnv(t)
	root, _, st := mountHardeningHost(t)

	// Both release flags are seeded OFF: tier 2 and tier 3 applies are refused with 409 conflict.
	for key, flag := range map[string]string{"fw.default_deny": "hardening.apply_tier2", "ssh.no_password": "hardening.apply_tier3"} {
		w, b := c.post("/hardening/apply", hardeningApplyBody(key, false))
		if w.Code != 409 || b["code"] != "conflict" {
			t.Fatalf("gated %s: want 409 conflict, got %d %v", key, w.Code, b)
		}
		if d, _ := b["details"].(map[string]any); d["flag"] != flag {
			t.Fatalf("gated %s: details.flag = %v, want %s", key, d["flag"], flag)
		}
	}
	// Tier 0 is never applyable.
	if w, b := c.post("/hardening/apply", hardeningApplyBody("net.exposed_services", false)); w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("tier 0 apply: %d %v", w.Code, b)
	}
	if w, b := c.post("/hardening/apply", hardeningApplyBody("nope", false)); w.Code != 422 {
		t.Fatalf("unknown key must be 422, got %d %v", w.Code, b)
	}
	// Tier 1 is not gated: without step-up the documented refusal is 403 step_up_required, and it lands
	// after every cheaper validation so a doomed request never opens the 2FA dialog.
	if w, b := c.post("/hardening/apply", hardeningApplyBody("kern.sysctl", false)); w.Code != 403 || b["code"] != "step_up_required" {
		t.Fatalf("tier 1 without step-up: %d %v", w.Code, b)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_changes`); n != 0 {
		t.Fatalf("refused applies recorded %d changes", n)
	}
	for _, name := range []string{"apt-get", "sysctl", "systemctl", "systemd-run", "nft"} {
		if st.has(name) {
			t.Fatalf("refused apply executed %s", name)
		}
	}
	if n := hardeningFileCount(t, root); n != 0 {
		t.Fatalf("refused apply wrote %d files", n)
	}
}

func TestHardeningApplyConfirmRevertLifecycle(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	root, revertDir, st := mountHardeningHost(t)
	hardeningStepUp(t, c, secret)

	var firstApt manifestAndJail
	st.onCall = func(name string, _ []string) {
		if name != "apt-get" || firstApt.checked {
			return
		}
		firstApt.checked = true
		_ = filepath.WalkDir(revertDir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Name() == "manifest.json" {
				firstApt.manifest = true
			}
			return nil
		})
		_, err := os.Stat(filepath.Join(root, "etc/fail2ban/jail.d/codeplo.local"))
		firstApt.jail = err == nil
	}

	w, resp := c.post("/hardening/apply", hardeningApplyBody("bf.protection", false))
	if w.Code != 200 {
		t.Fatalf("apply: %d %v", w.Code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("no change id: %v", resp)
	}
	if resp["state"] != "confirmed" || resp["check_name"] != "Brute-force protection" || int(resp["tier"].(float64)) != 1 {
		t.Fatalf("apply response: %v", resp)
	}
	if resp["revert_unit"] != nil {
		t.Fatalf("a Tier 1 change cannot cut access, so it must not carry a dead-man unit: %v", resp["revert_unit"])
	}
	if resp["reverted_at"] != nil {
		t.Fatalf("a fresh change must not carry a revert timestamp: %v", resp)
	}
	if resp["confirmed_at"] == nil || resp["confirmed_at"] != resp["applied_at"] {
		t.Fatalf("Tier 1 apply confirms immediately: %v", resp)
	}
	if dl := resp["confirm_deadline"]; dl != nil {
		t.Fatalf("Tier 1 has no confirm deadline, got %v", dl)
	}
	// Snapshot before the first host command, and the jail file only after it.
	if !firstApt.checked || !firstApt.manifest || firstApt.jail {
		t.Fatalf("snapshot-then-apply order broken: %+v", firstApt)
	}
	snapDir, _ := resp["snapshot_path"].(string)
	raw, err := os.ReadFile(filepath.Join(snapDir, "manifest.json"))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if !strings.Contains(string(raw), "/etc/fail2ban/jail.d/codeplo.local") {
		t.Fatalf("manifest paths: %s", raw)
	}
	jail, err := os.ReadFile(filepath.Join(root, "etc/fail2ban/jail.d/codeplo.local"))
	if err != nil {
		t.Fatalf("jail file not written: %v", err)
	}
	// The requesting admin IP must land on the ignore list (httptest RemoteAddr is 192.0.2.1).
	if !strings.Contains(string(jail), "ignoreip = 127.0.0.1/8 ::1 192.0.2.1") {
		t.Fatalf("jail ignoreip: %s", jail)
	}
	if st.has("systemd-run") {
		t.Fatalf("Tier 1 apply must never arm a dead-man timer: %v", st.calls)
	}
	if state := hardeningScalar(t, e, `SELECT state FROM hardening_changes WHERE id=?`, id); state != "confirmed" {
		t.Fatalf("db state: %v", state)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_changes WHERE id=? AND confirm_deadline IS NOT NULL AND revert_unit IS NOT NULL`, id); n != 0 {
		t.Fatal("an unguarded change must persist no deadline and no revert unit")
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_runs WHERE mode='apply'`); n != 1 {
		t.Fatalf("apply run rows = %d", n)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM audit_events WHERE action='hardening.applied' AND target_type='hardening_change' AND target_id=?`, id); n != 1 {
		t.Fatalf("hardening.applied audit rows = %d", n)
	}

	// Idempotent: re-applying the same check is refused with the existing change id, state untouched.
	w, b := c.post("/hardening/apply", hardeningApplyBody("bf.protection", false))
	if w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("re-apply must be 409 conflict, got %d %v", w.Code, b)
	}
	if d, _ := b["details"].(map[string]any); d["change_id"] != id {
		t.Fatalf("re-apply details: %v", b["details"])
	}

	// A Tier 1 change is confirmed by the apply itself: there is no connectivity to prove, so confirming again
	// conflicts and no dead-man is cancelled (there is none).
	w, resp = c.post("/hardening/confirm/"+id, nil)
	if w.Code != 409 || resp["code"] != "conflict" {
		t.Fatalf("confirm of an unguarded change must conflict, got %d %v", w.Code, resp)
	}
	if state := hardeningScalar(t, e, `SELECT state FROM hardening_changes WHERE id=?`, id); state != "confirmed" {
		t.Fatalf("db state after refused confirm: %v", state)
	}
	if st.called("systemctl", "stop", "codeplo-revert-"+id+".timer") {
		t.Fatalf("no timer to cancel: %v", st.calls)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM audit_events WHERE action='hardening.confirmed' AND target_id=?`, id); n != 0 {
		t.Fatalf("hardening.confirmed audit rows = %d, want 0", n)
	}

	// Revert works from confirmed too: the jail file (which did not exist before) is removed again.
	w, resp = c.post("/hardening/revert/"+id, nil)
	if w.Code != 200 || resp["state"] != "reverted" {
		t.Fatalf("revert from confirmed: %d %v", w.Code, resp)
	}
	if resp["reverted_at"] == nil || resp["reverted_at"] == "" {
		t.Fatal("reverted_at missing")
	}
	if _, err := os.Stat(filepath.Join(root, "etc/fail2ban/jail.d/codeplo.local")); !os.IsNotExist(err) {
		t.Fatalf("jail file must be reverted (err=%v)", err)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM audit_events WHERE action='hardening.reverted' AND target_id=?`, id); n != 1 {
		t.Fatalf("hardening.reverted audit rows = %d", n)
	}
	if w, b := c.post("/hardening/revert/"+id, nil); w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("revert of a reverted change must conflict, got %d %v", w.Code, b)
	}
}

type manifestAndJail struct {
	checked  bool
	manifest bool
	jail     bool
}

func TestHardeningRevertConfirmedTier1(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	root, _, st := mountHardeningHost(t)
	hardeningStepUp(t, c, secret)

	w, resp := c.post("/hardening/apply", hardeningApplyBody("kern.sysctl", false))
	if w.Code != 200 || resp["state"] != "confirmed" {
		t.Fatalf("apply: %d %v", w.Code, resp)
	}
	id, _ := resp["id"].(string)
	f := filepath.Join(root, "etc/sysctl.d/60-codeplo.conf")
	if _, err := os.Stat(f); err != nil {
		t.Fatalf("sysctl file not applied: %v", err)
	}

	w, resp = c.post("/hardening/revert/"+id, nil)
	if w.Code != 200 || resp["state"] != "reverted" {
		t.Fatalf("revert from pending: %d %v", w.Code, resp)
	}
	if resp["check_name"] != "Kernel network hardening" {
		t.Fatalf("check_name: %v", resp["check_name"])
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("sysctl file must be removed by revert (err=%v)", err)
	}
	if state := hardeningScalar(t, e, `SELECT state FROM hardening_changes WHERE id=?`, id); state != "reverted" {
		t.Fatalf("db state: %v", state)
	}
	// A reverted change can no longer be confirmed.
	if w, b := c.post("/hardening/confirm/"+id, nil); w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("confirm after revert must conflict, got %d %v", w.Code, b)
	}
	// Unknown ids are 404.
	if w, b := c.post("/hardening/confirm/hchg-nope", nil); w.Code != 404 || b["code"] != "not_found" {
		t.Fatalf("confirm unknown: %d %v", w.Code, b)
	}
	if w, b := c.post("/hardening/revert/hchg-nope", nil); w.Code != 404 || b["code"] != "not_found" {
		t.Fatalf("revert unknown: %d %v", w.Code, b)
	}
	if st.has("systemd-run") {
		t.Fatal("tier 1 apply must never arm the dead-man timer (plan 3.10: only tiers 2-3 can cut access)")
	}
}

func TestHardeningApplyRefusedWhenPreflightFails(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	root, _, st := mountHardeningHost(t)
	if _, err := e.s.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key='hardening.apply_tier3'`); err != nil {
		t.Fatal(err)
	}
	hardeningStepUp(t, c, secret)

	// Tier 3 without the console acknowledgement is a validation failure (before any host touch).
	if w, b := c.post("/hardening/apply", hardeningApplyBody("ssh.no_password", false)); w.Code != 422 || b["code"] != "validation_failed" {
		t.Fatalf("missing ack: %d %v", w.Code, b)
	}
	// With the ack, the unproven-key-login fixture tree fails preflight: 409 preflight_failed with the
	// failing items in details.checks, and nothing changes on the host.
	w, b := c.post("/hardening/apply", hardeningApplyBody("ssh.no_password", true))
	if w.Code != 409 || b["code"] != "preflight_failed" {
		t.Fatalf("unproven key login must refuse apply, got %d %v", w.Code, b)
	}
	if d, _ := b["details"].(map[string]any); d["checks"] == nil {
		t.Fatalf("preflight_failed must carry details.checks: %v", b["details"])
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_changes`); n != 0 {
		t.Fatalf("refused apply recorded %d changes", n)
	}
	if hardeningFileCount(t, root) != 0 {
		t.Fatal("refused apply wrote files")
	}
	if st.has("systemd-run") {
		t.Fatal("refused apply armed a timer")
	}
}

func TestHardeningTier2ApplyIsRuntimeFirst(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	root, revertDir, st := mountHardeningHost(t)
	if _, err := e.s.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key='hardening.apply_tier2'`); err != nil {
		t.Fatal(err)
	}
	hardeningStepUp(t, c, secret)

	w, resp := c.post("/hardening/apply", hardeningApplyBody("fw.default_deny", false))
	if w.Code != 200 {
		t.Fatalf("tier 2 apply: %d %v", w.Code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("no change id: %v", resp)
	}
	// Runtime-first: the rendered ruleset is loaded with nft -f at apply time…
	if !st.called("nft", "-f") {
		t.Fatalf("nft not executed: %v", st.calls)
	}
	nftFile := filepath.Join(root, "var/lib/codeplo/plan/codeplo.nft")
	raw, err := os.ReadFile(nftFile)
	if err != nil {
		t.Fatalf("ruleset file not written: %v", err)
	}
	if !strings.Contains(string(raw), "table inet codeplo") {
		t.Fatalf("ruleset: %s", raw)
	}
	// …and the change waits for confirmation like every other apply.
	if resp["state"] != "pending_confirm" || resp["revert_unit"] != "codeplo-revert-"+id {
		t.Fatalf("response: %v", resp)
	}
	// Apply writes nothing under /etc: the ruleset is live in the kernel only, so rebooting before confirm
	// returns the host to its prior state (plan 3.10.2).
	if _, err := os.Stat(filepath.Join(root, "etc/nftables/codeplo.nft")); !os.IsNotExist(err) {
		t.Fatalf("apply persisted the ruleset before confirmation: %v", err)
	}
	// The snapshot records how to undo the live table, which is what makes the daemon-free dead-man a real
	// revert rather than a file restore.
	rj, err := os.ReadFile(filepath.Join(revertDir, id, hardening.RevertCommandsFile))
	if err != nil {
		t.Fatalf("apply recorded no live-state revert: %v", err)
	}
	if !strings.Contains(string(rj), `"delete"`) || !strings.Contains(string(rj), "codeplo") {
		t.Fatalf("revert steps: %s", rj)
	}
	// Confirm persists the ruleset for boot and only then disarms the dead-man.
	if w, b := c.post("/hardening/confirm/"+id, nil); w.Code != 200 || b["state"] != "confirmed" {
		t.Fatalf("confirm: %d %v", w.Code, b)
	}
	boot, err := os.ReadFile(filepath.Join(root, "etc/nftables/codeplo.nft"))
	if err != nil {
		t.Fatalf("confirm did not persist the table: %v", err)
	}
	if !strings.Contains(string(boot), "table inet codeplo") || strings.Contains(string(boot), "destroy table") {
		t.Fatalf("boot ruleset: %s", boot)
	}
	conf, err := os.ReadFile(filepath.Join(root, "etc/nftables.conf"))
	if err != nil || !strings.Contains(string(conf), "include /etc/nftables/codeplo.nft") {
		t.Fatalf("boot conf not extended: %v %s", err, conf)
	}
	if !st.called("systemctl", "enable", "nftables") {
		t.Fatalf("nftables service not enabled: %v", st.calls)
	}
	stop := st.indexOf("systemctl", "stop", "codeplo-revert-"+id+".timer")
	if stop < 0 {
		t.Fatalf("timer not cancelled: %v", st.calls)
	}
	if enable := st.indexOf("systemctl", "enable", "nftables"); enable > stop {
		t.Fatalf("the dead-man was disarmed before the change was persisted: %v", st.calls)
	}
}

// TestHardeningTier2RevertDropsLiveTable proves the API revert matches the CLI one: restoring the snapshot
// files must also take down table inet codeplo, or a "reverted" firewall is still blocking.
func TestHardeningTier2RevertDropsLiveTable(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	root, _, st := mountHardeningHost(t)
	if _, err := e.s.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key='hardening.apply_tier2'`); err != nil {
		t.Fatal(err)
	}
	hardeningStepUp(t, c, secret)

	w, resp := c.post("/hardening/apply", hardeningApplyBody("fw.default_deny", false))
	if w.Code != 200 {
		t.Fatalf("tier 2 apply: %d %v", w.Code, resp)
	}
	id, _ := resp["id"].(string)
	nftFile := filepath.Join(root, "var/lib/codeplo/plan/codeplo.nft")
	if _, err := os.Stat(nftFile); err != nil {
		t.Fatalf("ruleset file missing before revert: %v", err)
	}
	if w, b := c.post("/hardening/revert/"+id, nil); w.Code != 200 || b["state"] != "reverted" {
		t.Fatalf("revert: %d %v", w.Code, b)
	}
	if !st.called("nft", "delete", "table inet codeplo") {
		t.Fatalf("live table not removed: %v", st.calls)
	}
	if _, err := os.Stat(nftFile); !os.IsNotExist(err) {
		t.Fatalf("revert left the ruleset file behind: %v", err)
	}
	// Reverting a pending change also cancels the dead-man: an uncancelled timer would fire against a host
	// that is already back to its snapshot.
	if !st.called("systemctl", "stop", "codeplo-revert-"+id+".timer") {
		t.Fatalf("timer not cancelled: %v", st.calls)
	}
}

// TestHardeningRevertIsIdempotentWhenTheTableIsGone covers the probe: an `nft list` that fails means the table
// is already absent, so revert must skip the delete instead of guessing, and must still succeed.
func TestHardeningRevertIsIdempotentWhenTheTableIsGone(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	_, _, st := mountHardeningHost(t)
	if _, err := e.s.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key='hardening.apply_tier2'`); err != nil {
		t.Fatal(err)
	}
	hardeningStepUp(t, c, secret)
	// The revert probe (`nft list table inet codeplo`) fails while the preflight read (`nft list ruleset`)
	// still succeeds: the table is already gone, which is the only situation this test claims.
	st.failRun = func(name string, args []string) bool {
		return name == "nft" && len(args) >= 2 && args[0] == "list" && args[1] == "table"
	}

	w, resp := c.post("/hardening/apply", hardeningApplyBody("fw.default_deny", false))
	if w.Code != 200 {
		t.Fatalf("apply: %d %v", w.Code, resp)
	}
	id, _ := resp["id"].(string)
	if w, b := c.post("/hardening/revert/"+id, nil); w.Code != 200 || b["state"] != "reverted" {
		t.Fatalf("revert with the table already gone: %d %v", w.Code, b)
	}
	if st.called("nft", "delete") {
		t.Fatalf("revert deleted a table the probe reported absent: %v", st.calls)
	}
}

// TestHardeningTier2PendingGuard covers the pending-collision rule (plan 3.10): while a guarded change is
// waiting for confirmation, no other access-cutting change may start. Tier 1 is not part of this: it confirms
// at apply time and holds no pending slot (see TestHardeningRevertConfirmedTier1).
func TestHardeningTier2PendingGuard(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	mountHardeningHost(t)
	for _, flag := range []string{"hardening.apply_tier2", "hardening.apply_tier3"} {
		if _, err := e.s.DB.Exec(`UPDATE feature_flags SET enabled=1 WHERE key=?`, flag); err != nil {
			t.Fatal(err)
		}
	}
	hardeningStepUp(t, c, secret)

	w, resp := c.post("/hardening/apply", hardeningApplyBody("fw.default_deny", false))
	if w.Code != 200 || resp["state"] != "pending_confirm" {
		t.Fatalf("tier 2 apply: %d %v", w.Code, resp)
	}
	// A change is waiting for confirmation: the next access-cutting apply refuses before planning or
	// pre-flighting, so nothing can start while the host is only half-decided.
	w, b := c.post("/hardening/apply", hardeningApplyBody("ssh.no_password", true))
	if w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("tier 3 while a change is pending: %d %v", w.Code, b)
	}
	if m, _ := b["message"].(string); !strings.Contains(m, "waiting for confirmation") {
		t.Fatalf("expected the pending-collision message, got %q", m)
	}
	// Confirming clears the slot for the next guarded apply.
	id, _ := resp["id"].(string)
	if w, b := c.post("/hardening/confirm/"+id, nil); w.Code != 200 || b["state"] != "confirmed" {
		t.Fatalf("confirm: %d %v", w.Code, b)
	}
	if w, b := c.post("/hardening/apply", hardeningApplyBody("kern.sysctl", false)); w.Code != 200 || b["state"] != "confirmed" {
		t.Fatalf("tier 1 after the guarded change resolved: %d %v", w.Code, b)
	}
}

func TestHardeningApplyAuditAndStateCommitAtomically(t *testing.T) {
	e, c, secret := newHardeningEnv(t)
	mountHardeningHost(t)
	hardeningStepUp(t, c, secret)

	if _, err := e.s.DB.Exec(`CREATE TRIGGER audit_fail_hardening_applied BEFORE INSERT ON audit_events
		WHEN NEW.action='hardening.applied' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if w, b := c.post("/hardening/apply", hardeningApplyBody("kern.sysctl", false)); w.Code != 500 || b["code"] != "internal" {
		t.Fatalf("injected audit failure must be 500, got %d %v", w.Code, b)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_changes`); n != 0 {
		t.Fatalf("change row must roll back with its audit row (got %d)", n)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM hardening_runs WHERE mode='apply'`); n != 0 {
		t.Fatalf("apply run row must roll back too (got %d)", n)
	}
	if n := hardeningCount(t, e, `SELECT COUNT(*) FROM audit_events WHERE action='hardening.applied'`); n != 0 {
		t.Fatalf("audit row must roll back (got %d)", n)
	}
	if r, err := audit.Verify(context.Background(), e.s.DB); err != nil || !r.Valid {
		t.Fatalf("chain must stay valid after the rolled-back write: %+v %v", r, err)
	}
	if _, err := e.s.DB.Exec(`DROP TRIGGER audit_fail_hardening_applied`); err != nil {
		t.Fatal(err)
	}
	// The same request succeeds once the injected failure is gone.
	if w, b := c.post("/hardening/apply", hardeningApplyBody("kern.sysctl", false)); w.Code != 200 {
		t.Fatalf("retry after dropping the trigger must succeed, got %d %v", w.Code, b)
	}
}
