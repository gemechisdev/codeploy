package api

import (
	"context"
	"database/sql"
	"net/http"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/hardening"
	"github.com/codeplo/codeplo/internal/store"
)

// hardeningRoutes are the implemented P2a routes; they override the matching later-phase stubs.
func (s *Server) hardeningRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/hardening/checks", Perm: "hardening:read", Module: "hardening", Handler: s.hardeningChecks},
		{Method: "POST", Path: "/hardening/audit", Perm: "hardening:read", Module: "hardening", Handler: s.hardeningAudit},
		{Method: "GET", Path: "/hardening/runs", Perm: "hardening:read", Module: "hardening", Handler: s.hardeningRuns},
		{Method: "GET", Path: "/hardening/changes", Perm: "hardening:read", Module: "hardening", Handler: s.hardeningChanges},
		{Method: "POST", Path: "/hardening/plan", Perm: "hardening:read", Module: "hardening", Handler: s.hardeningPlan},
		{Method: "POST", Path: "/hardening/preflight", Perm: "hardening:read", Module: "hardening", Handler: s.hardeningPreflight},
		{Method: "POST", Path: "/hardening/apply", Perm: "hardening:apply", Module: "hardening", Handler: s.hardeningApply},
		{Method: "POST", Path: "/hardening/confirm/{changeId}", Perm: "hardening:apply", Module: "hardening", Handler: s.hardeningConfirm},
		{Method: "POST", Path: "/hardening/revert/{changeId}", Perm: "hardening:apply", Module: "hardening", Handler: s.hardeningRevert},
	}
}

func (s *Server) moduleOn(w http.ResponseWriter, r *http.Request, key string) bool {
	if !s.Reg.Enabled(r.Context(), key) {
		writeErr(w, 404, "module_disabled", "The "+key+" module is disabled", map[string]any{"module": key})
		return false
	}
	return true
}

func (s *Server) applyFlags(r *http.Request) map[string]bool {
	m := map[string]bool{}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT key, enabled FROM feature_flags WHERE key LIKE 'hardening.%'`)
	if err != nil {
		return m
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var e int
		if rows.Scan(&k, &e) == nil {
			m[k] = e == 1
		}
	}
	return m
}

// attachPlans fills the fields the Security page needs to offer a fix: a tier>=1 check carries its planned diff
// (the UI shows "Plan & apply" only when plan_diff is present) and, for Tier 1, whether there is anything to
// apply at all (a passing check has nothing to do). The planner runs without a command runner: a GET must never
// exec sshd/nft, so SSHPorts falls back to reading sshd_config text.
func (s *Server) attachPlans(ctx context.Context, cs []hardening.Check) {
	planner := &hardening.Planner{Env: hardening.Env{Root: hardeningRoot}}
	var op sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT operator FROM setup_state WHERE id=1`).Scan(&op)
	prm := hardening.Params{Operator: op.String}
	for i := range cs {
		if cs[i].Tier < 1 {
			continue
		}
		if cs[i].Tier == 1 {
			cs[i].ApplyEnabled = cs[i].Status != hardening.Pass
		}
		if pln, err := planner.Plan(ctx, cs[i].Key, prm); err == nil {
			cs[i].PlanDiff = pln.Diff
		}
	}
}

// hardeningChecks serves the last stored audit result; it never runs detectors (use POST /hardening/audit).
func (s *Server) hardeningChecks(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	ctx := r.Context()
	env := hardening.Env{Root: hardeningRoot}
	var runID, at string
	var score int
	err := s.DB.QueryRowContext(ctx, `SELECT id, COALESCE(finished_at, started_at), score FROM hardening_runs WHERE mode='audit' ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&runID, &at, &score)
	if err == sql.ErrNoRows {
		cs := hardening.Run(env, s.applyFlags(r)) // detect-only view, not persisted
		for i := range cs {
			cs[i].Status = hardening.Skipped
			cs[i].DetectDetail = "not audited yet"
		}
		s.attachPlans(ctx, cs)
		writeJSON(w, 200, map[string]any{"items": cs, "score": 0, "last_run_at": nil})
		return
	} else if err != nil {
		internalErr(w, err)
		return
	}
	cs := hardening.Run(env, s.applyFlags(r))
	stored := map[string][2]string{}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT check_key, status, detail FROM hardening_results WHERE run_id=?`, runID)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var k, st, d string
		if rows.Scan(&k, &st, &d) == nil {
			stored[k] = [2]string{st, d}
		}
	}
	for i := range cs {
		if v, ok := stored[cs[i].Key]; ok {
			cs[i].Status, cs[i].DetectDetail = hardening.Status(v[0]), v[1]
		}
	}
	s.attachPlans(ctx, cs)
	writeJSON(w, 200, map[string]any{"items": cs, "score": score, "last_run_at": at})
}

// hardeningAudit runs the read-only detectors, records the run and appends one audit row in the same transaction.
func (s *Server) hardeningAudit(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	p := from(r)
	ctx := r.Context()
	started := now()
	cs := hardening.Run(hardening.Env{Root: hardeningRoot}, s.applyFlags(r)) // detection touches nothing
	s.attachPlans(ctx, cs)
	score := hardening.Score(cs)
	runID, finished := newID("hrun"), now()
	err := store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO hardening_runs(id,mode,started_at,finished_at,actor_id,score) VALUES (?,?,?,?,?,?)`, runID, "audit", started, finished, p.User.ID, score); err != nil {
			return err
		}
		for _, c := range cs {
			if _, err := tx.Exec(`INSERT INTO hardening_results(run_id,check_key,tier,status,detail) VALUES (?,?,?,?,?)`, runID, c.Key, c.Tier, string(c.Status), c.DetectDetail); err != nil {
				return err
			}
		}
		_, err := audit.Append(r.Context(), tx, audit.Entry{ActorType: "user", ActorID: p.User.ID, ActorEmail: p.User.Email, IP: clientIP(r),
			Action: "hardening.audit", TargetType: "hardening_run", TargetID: runID, Meta: map[string]any{"score": score}})
		return err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("audit.appended", map[string]any{"action": "hardening.audit"})
	writeJSON(w, 200, map[string]any{"items": cs, "score": score, "last_run_at": finished})
}

func (s *Server) hardeningRuns(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	limit, cursor := page(r)
	q := `SELECT id, mode, started_at, finished_at, actor_id, score FROM hardening_runs`
	args := []any{}
	if cursor != "" {
		q += ` WHERE id < ?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(r.Context(), q, args...)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, mode, st, actor string
		var fin sql.NullString
		var score int
		if err := rows.Scan(&id, &mode, &st, &fin, &actor, &score); err != nil {
			internalErr(w, err)
			return
		}
		var f any
		if fin.Valid {
			f = fin.String
		}
		items = append(items, map[string]any{"id": id, "mode": mode, "started_at": st, "finished_at": f, "actor_id": actor, "score": score})
	}
	var next any
	if len(items) > limit {
		next = items[limit-1]["id"]
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

// hardeningChanges: no apply path exists yet (Tier 1 apply is open work), so this lists stored rows only.
func (s *Server) hardeningChanges(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "hardening") {
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, check_key, tier, snapshot_path, revert_unit, applied_at, confirm_deadline, confirmed_at, reverted_at, state FROM hardening_changes ORDER BY applied_at DESC LIMIT 100`)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, key, snap, applied, state string
		var tier int
		var unit, dl, conf, rev sql.NullString
		if err := rows.Scan(&id, &key, &tier, &snap, &unit, &applied, &dl, &conf, &rev, &state); err != nil {
			internalErr(w, err)
			return
		}
		ns := func(n sql.NullString) any {
			if n.Valid {
				return n.String
			}
			return nil
		}
		items = append(items, map[string]any{"id": id, "check_key": key, "check_name": key, "tier": tier, "snapshot_path": snap,
			"revert_unit": ns(unit), "applied_at": applied, "confirm_deadline": ns(dl), "confirmed_at": ns(conf), "reverted_at": ns(rev), "state": state})
	}
	writeJSON(w, 200, map[string]any{"items": items, "server_now": now()})
}
