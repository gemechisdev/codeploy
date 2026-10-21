package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/authn"
	"github.com/codeplo/codeplo/internal/authz"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/events"
	"github.com/codeplo/codeplo/internal/modules/monitor"
	"github.com/codeplo/codeplo/internal/platform"
)

// ---------------- users ----------------

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	limit, cursor := page(r)
	rows, err := s.DB.QueryContext(r.Context(), `SELECT `+userCols+` FROM users u WHERE u.id > ? ORDER BY u.id LIMIT ?`, cursor, limit+1)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var ids []string
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, u.Public())
		ids = append(ids, u.ID)
	}
	var next any
	if len(items) > limit {
		items, next = items[:limit], ids[limit-1]
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) roleExists(r *http.Request, role string) bool {
	var n int
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM roles WHERE key=?`, role).Scan(&n)
	return n == 1
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	if !requireStepUp(w, p) {
		return
	}
	var b struct{ Email, Password, Role string }
	if !decode(w, r, &b) {
		return
	}
	f := map[string]string{}
	if !emailRe.MatchString(b.Email) {
		f["email"] = "Enter a valid email address"
	}
	if len(b.Password) < authn.MinPasswordLen {
		f["password"] = "Use at least 12 characters"
	}
	if b.Role == "owner" || !s.roleExists(r, b.Role) {
		f["role"] = "Choose admin, deployer or viewer"
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	hash, err := authn.HashPassword(b.Password)
	if err != nil {
		internalErr(w, err)
		return
	}
	u := User{ID: newID("usr_"), Email: b.Email, Role: b.Role, CreatedAt: now()}
	dup := false
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var n int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM users WHERE email=?`, b.Email).Scan(&n)
		if n > 0 {
			dup = true
			return audit.Event{}, sql.ErrNoRows
		}
		if _, err := tx.Exec(`INSERT INTO users(id,email,password_hash,role,created_at) VALUES (?,?,?,?,?)`, u.ID, u.Email, hash, u.Role, u.CreatedAt); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "user.created", "user", u.ID, map[string]any{"role": u.Role})
	})
	if dup {
		writeErr(w, 409, "conflict", "A user with this email already exists", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 201, u.Public())
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	if !requireStepUp(w, p) {
		return
	}
	var b struct {
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	id := chi.URLParam(r, "id")
	var out User
	var code, msg string
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		u, err := scanUser(tx.QueryRow(`SELECT `+userCols+` FROM users u WHERE u.id=?`, id))
		if err != nil {
			code, msg = "not_found", "user not found"
			return audit.Event{}, sql.ErrNoRows
		}
		switch {
		case u.Role == "owner" && p.User.Role != "owner":
			code, msg = "forbidden", "Only an owner can change an owner"
		case u.ID == p.User.ID && b.Disabled != nil && *b.Disabled:
			code, msg = "conflict", "You cannot disable your own account"
		case b.Role != nil && u.Role == "owner":
			code, msg = "conflict", "The owner role cannot be changed"
		case b.Role != nil && (*b.Role == "owner" || !s.roleExists(r, *b.Role)):
			code, msg = "validation_failed", "Choose admin, deployer or viewer"
		}
		if code != "" {
			return audit.Event{}, sql.ErrNoRows
		}
		if b.Role != nil {
			u.Role = *b.Role
		}
		if b.Disabled != nil {
			u.Disabled = *b.Disabled
		}
		if _, err := tx.Exec(`UPDATE users SET role=?, disabled=? WHERE id=?`, u.Role, b2i(u.Disabled), u.ID); err != nil {
			return audit.Event{}, err
		}
		if u.Disabled { // revoke sessions immediately
			if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id=?`, u.ID); err != nil {
				return audit.Event{}, err
			}
		}
		out = u
		return s.rec(r.Context(), tx, r, p, "user.updated", "user", u.ID, map[string]any{"role": u.Role, "disabled": u.Disabled})
	})
	if code != "" {
		status := map[string]int{"not_found": 404, "forbidden": 403, "conflict": 409, "validation_failed": 422}[code]
		writeErr(w, status, code, msg, nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, out.Public())
}

// ---------------- tokens ----------------

func tokenView(id, name, prefix, scopes string, used, exp *string, created string) map[string]any {
	return map[string]any{"id": id, "name": name, "prefix": prefix, "scopes": strings.Split(scopes, ","), "last_used_at": used, "expires_at": exp, "created_at": created}
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	limit, cursor := page(r)
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,name,prefix,scopes,last_used_at,expires_at,created_at FROM api_tokens WHERE id > ? ORDER BY id LIMIT ?`, cursor, limit+1)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	var ids []string
	for rows.Next() {
		var id, name, prefix, scopes, created string
		var used, exp *string
		if err := rows.Scan(&id, &name, &prefix, &scopes, &used, &exp, &created); err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, tokenView(id, name, prefix, scopes, used, exp, created))
		ids = append(ids, id)
	}
	var next any
	if len(items) > limit {
		items, next = items[:limit], ids[limit-1]
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	var b struct {
		Name      string   `json:"name"`
		Scopes    []string `json:"scopes"`
		ExpiresAt *string  `json:"expires_at"`
	}
	if !decode(w, r, &b) {
		return
	}
	f := map[string]string{}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		f["name"] = "Give the token a name"
	}
	if len(b.Scopes) == 0 {
		f["scopes"] = "Select at least one scope"
	}
	for _, sc := range b.Scopes {
		if !authz.Valid(sc) || sc == authz.Public || sc == authz.Session {
			f["scopes"] = "Unknown scope"
		} else if !p.Perms[sc] {
			f["scopes"] = "You cannot grant scopes you do not hold"
		}
	}
	if b.ExpiresAt != nil {
		if t, err := time.Parse(time.RFC3339, *b.ExpiresAt); err != nil || !t.After(time.Now()) {
			f["expires_at"] = "Must be a future ISO-8601 timestamp"
		}
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	secret := "cpl_" + crypto.RandomHex(20)
	id := newID("tok_")
	created := now()
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.Exec(`INSERT INTO api_tokens(id,name,prefix,hash,scopes,user_id,expires_at,created_at) VALUES (?,?,?,?,?,?,?,?)`,
			id, b.Name, secret[:8], crypto.HashToken(secret), strings.Join(b.Scopes, ","), p.User.ID, b.ExpiresAt, created); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "token.created", "token", id, map[string]any{"scopes": b.Scopes})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	// The secret is returned exactly once; only its SHA-256 is stored.
	writeJSON(w, 201, map[string]any{"token": tokenView(id, b.Name, secret[:8], strings.Join(b.Scopes, ","), nil, b.ExpiresAt, created), "secret": secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	p, id := from(r), chi.URLParam(r, "id")
	missing := false
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		res, err := tx.Exec(`DELETE FROM api_tokens WHERE id=?`, id)
		if err != nil {
			return audit.Event{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			missing = true
			return audit.Event{}, sql.ErrNoRows
		}
		return s.rec(r.Context(), tx, r, p, "token.revoked", "token", id, nil)
	})
	if missing {
		writeErr(w, 404, "not_found", "token not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// ---------------- roles / permissions ----------------

func (s *Server) roles(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT key,name,builtin FROM roles ORDER BY CASE key WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 WHEN 'deployer' THEN 2 WHEN 'viewer' THEN 3 ELSE 4 END, key`)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var k, n string
		var b int
		if err := rows.Scan(&k, &n, &b); err != nil {
			internalErr(w, err)
			return
		}
		perms := []string{}
		for pm := range s.rolePerms(r.Context(), k) {
			perms = append(perms, pm)
		}
		sort.Strings(perms)
		items = append(items, map[string]any{"key": k, "name": n, "builtin": b == 1, "permissions": perms})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) permissions(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	for _, k := range authz.All() {
		items = append(items, map[string]any{"key": k, "description": authz.Doc(k)})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// ---------------- audit ----------------

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, cursor := page(r)
	q := r.URL.Query()
	where, args := []string{"1=1"}, []any{}
	if cursor != "" {
		where, args = append(where, "seq < ?"), append(args, cursor)
	}
	if v := q.Get("actor"); v != "" {
		where, args = append(where, "lower(COALESCE(actor_email, actor_id)) LIKE ?"), append(args, "%"+strings.ToLower(v)+"%")
	}
	if v := q.Get("action"); v != "" {
		where, args = append(where, "lower(action) LIKE ?"), append(args, "%"+strings.ToLower(v)+"%")
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			where, args = append(where, "at >= ?"), append(args, iso(t))
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			where, args = append(where, "at <= ?"), append(args, iso(t))
		}
	}
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(r.Context(), `SELECT seq,id,at,actor_type,actor_id,actor_email,ip,action,target_type,target_id,meta,prev_hash,hash FROM audit_events WHERE `+strings.Join(where, " AND ")+` ORDER BY seq DESC LIMIT ?`, args...)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []audit.Event{}
	for rows.Next() {
		var e audit.Event
		var meta string
		if err := rows.Scan(&e.Seq, &e.ID, &e.At, &e.ActorType, &e.ActorID, &e.ActorEmail, &e.IP, &e.Action, &e.TargetType, &e.TargetID, &meta, &e.PrevHash, &e.Hash); err != nil {
			internalErr(w, err)
			return
		}
		_ = json.Unmarshal([]byte(meta), &e.Meta)
		items = append(items, e)
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		next = fmt.Sprint(items[limit-1].Seq)
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

func (s *Server) verifyAudit(w http.ResponseWriter, r *http.Request) {
	res, err := audit.Verify(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	p := from(r)
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "audit.verify", "audit", "chain", map[string]any{"valid": res.Valid, "verified_rows": res.VerifiedRows})
	}); err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// ---------------- system ----------------

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	h := platform.Host()
	var op sql.NullString
	_ = s.DB.QueryRowContext(r.Context(), `SELECT operator FROM setup_state WHERE id=1`).Scan(&op)
	tier := "headroom"
	switch mb := h.RAMTotalMB; {
	case mb < 1024:
		tier = "experimental"
	case mb < 2048:
		tier = "limited"
	case mb < 4096:
		tier = "primary"
	case mb < 8192:
		tier = "comfortable"
	}
	var opu any
	if op.Valid && op.String != "" {
		opu = op.String
	}
	// The card shows the newest sample the sampler took, because CPU percentage and network rate are differences
	// between two readings and a live grab would report zero where the history has a number. Until the first sample
	// exists — a fresh install, or a daemon whose loops have not started — the host's own instantaneous readings
	// stand in, which is honest where the previous all-zero metrics were not.
	metrics := monitor.Metric{MemoryUsedMB: h.RAMUsedMB, MemoryTotalMB: h.RAMTotalMB, DaemonRSSMB: float64(h.DaemonRSSMB),
		DiskUsedGB: h.DiskUsedGB, DiskTotalGB: h.DiskTotalGB, At: now()}
	if s.Monitor != nil {
		if pt, ok, err := s.Monitor.Latest(r.Context(), s.DB, monitor.ScopeHost, ""); err == nil && ok {
			metrics = pt
		}
	}
	// Both numbers are readings of stored state rather than live work: the score is the last audit run's, and a
	// pending change is one applied but not yet confirmed, which is the state that can still roll back.
	var score, pending int
	_ = s.DB.QueryRowContext(r.Context(), `SELECT score FROM hardening_runs WHERE mode='audit' ORDER BY started_at DESC, id DESC LIMIT 1`).Scan(&score)
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM hardening_changes WHERE state='pending_confirm'`).Scan(&pending)
	active := 0
	if s.Monitor != nil {
		if n, err := s.Monitor.ActiveRuleCount(r.Context(), s.DB); err == nil {
			active = n
		}
	}
	writeJSON(w, 200, map[string]any{
		"hostname": h.Hostname, "os": h.OS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(), "ram_total_mb": h.RAMTotalMB,
		"support_tier": tier, "uptime_s": int(time.Since(s.Started).Seconds()), "metrics": metrics,
		"operator_configured": opu != nil, "operator_user": opu, "security_score": score, "pending_changes": pending,
		"active_alerts": active, "server_now": now(),
	})
}

func (s *Server) systemVersion(w http.ResponseWriter, r *http.Request) {
	var schema int
	_ = s.DB.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(version_id),0) FROM goose_db_version`).Scan(&schema)
	writeJSON(w, 200, map[string]any{"version": s.Version, "channel": "stable", "commit": s.Commit, "built_at": s.BuiltAt, "go_version": runtime.Version(), "db_schema": schema})
}

// ---------------- modules / flags / settings ----------------

func (s *Server) moduleRows(r *http.Request, key string) ([]map[string]any, error) {
	q := `SELECT key,name,description,enabled,core,version,phase FROM modules`
	args := []any{}
	if key != "" {
		q, args = q+` WHERE key=?`, append(args, key)
	}
	rows, err := s.DB.QueryContext(r.Context(), q+` ORDER BY ord`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var k, n, d, v, ph string
		var e, c int
		if err := rows.Scan(&k, &n, &d, &e, &c, &v, &ph); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"key": k, "name": n, "description": d, "enabled": e == 1, "core": c == 1, "version": v, "phase": ph})
	}
	return out, nil
}

func (s *Server) listModules(w http.ResponseWriter, r *http.Request) {
	items, err := s.moduleRows(r, "")
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) patchModule(w http.ResponseWriter, r *http.Request) {
	p, key := from(r), chi.URLParam(r, "key")
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		invalid(w, map[string]string{"enabled": "enabled must be true or false"})
		return
	}
	var code, msg string
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var name string
		var core int
		if err := tx.QueryRow(`SELECT name,core FROM modules WHERE key=?`, key).Scan(&name, &core); err != nil {
			code, msg = "not_found", "module not found"
			return audit.Event{}, sql.ErrNoRows
		}
		if core == 1 && !*b.Enabled {
			code, msg = "conflict", `"`+name+`" is a core module and cannot be disabled`
			return audit.Event{}, sql.ErrNoRows
		}
		if _, err := tx.Exec(`UPDATE modules SET enabled=? WHERE key=?`, b2i(*b.Enabled), key); err != nil {
			return audit.Event{}, err
		}
		act := "module.disabled"
		if *b.Enabled {
			act = "module.enabled"
		}
		return s.rec(r.Context(), tx, r, p, act, "module", key, nil)
	})
	if code != "" {
		writeErr(w, map[string]int{"not_found": 404, "conflict": 409}[code], code, msg, nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("module.changed", map[string]any{"key": key})
	rows, _ := s.moduleRows(r, key)
	writeJSON(w, 200, rows[0])
}

func (s *Server) flagRows(r *http.Request) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT key,description,enabled,release_gated FROM feature_flags ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var k, d string
		var e, g int
		if err := rows.Scan(&k, &d, &e, &g); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"key": k, "description": d, "enabled": e == 1, "release_gated": g == 1})
	}
	return out, nil
}

func (s *Server) listFlags(w http.ResponseWriter, r *http.Request) {
	items, err := s.flagRows(r)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) patchFlags(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	var b struct {
		Flags map[string]any `json:"flags"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Flags == nil {
		invalid(w, map[string]string{"flags": "flags is required"})
		return
	}
	f := map[string]string{}
	cur := map[string]bool{}
	rows, _ := s.flagRows(r)
	for _, x := range rows {
		cur[x["key"].(string)] = x["enabled"].(bool)
	}
	for k, v := range b.Flags {
		if _, ok := cur[k]; !ok {
			f[k] = "Unknown flag"
		} else if _, ok := v.(bool); !ok {
			f[k] = "Must be true or false"
		}
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	var changed []string
	var appended []audit.Event
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		for k, v := range b.Flags {
			on := v.(bool)
			if cur[k] == on {
				continue
			}
			if _, e := tx.Exec(`UPDATE feature_flags SET enabled=? WHERE key=?`, b2i(on), k); e != nil {
				return audit.Event{}, e
			}
			ev, e := s.rec(r.Context(), tx, r, p, "flag.changed", "flag", k, map[string]any{"enabled": on})
			if e != nil {
				return audit.Event{}, e
			}
			appended = append(appended, ev)
			changed = append(changed, k)
		}
		return audit.Event{}, nil
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	// The mock publishes audit events only after commit, one per row.
	for _, ev := range appended {
		s.published(ev)
	}
	for _, k := range changed {
		s.Bus.Publish("flag.changed", map[string]any{"key": k})
	}
	items, _ := s.flagRows(r)
	writeJSON(w, 200, map[string]any{"items": items})
}

type settingRule struct {
	kind     string // boolean | int | number
	min, max float64
}

var settingRules = map[string]settingRule{
	"terminal.allow_root":         {"boolean", 0, 0},
	"terminal.idle_timeout_s":     {"int", 60, 3600},
	"retain_images":               {"int", 1, 20},
	"observation_window_s":        {"int", 10, 600},
	"step_up_window_s":            {"int", 60, 900},
	"hardening_revert_window_s":   {"int", 30, 900},
	"disk_preflight_threshold_gb": {"number", 1, 100},
	"session_ttl_h":               {"int", 1, 72},
}

func (s *Server) settingsMap(r *http.Request) (map[string]any, error) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]any{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		var x any
		_ = json.Unmarshal([]byte(v), &x)
		out[k] = x
	}
	return out, nil
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.settingsMap(r)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"values": m})
}

func (s *Server) patchSettings(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	var b struct {
		Values map[string]any `json:"values"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Values == nil {
		invalid(w, map[string]string{"values": "values is required"})
		return
	}
	f := map[string]string{}
	for k, v := range b.Values {
		rule, ok := settingRules[k]
		if !ok {
			f[k] = "Unknown setting"
			continue
		}
		if rule.kind == "boolean" {
			if _, ok := v.(bool); !ok {
				f[k] = "Must be true or false"
			}
			continue
		}
		n, ok := v.(float64)
		switch {
		case !ok:
			f[k] = "Must be a number"
		case rule.kind == "int" && n != float64(int64(n)):
			f[k] = "Must be a number"
		case n < rule.min || n > rule.max:
			f[k] = fmt.Sprintf("Must be between %v and %v", rule.min, rule.max)
		}
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		keys := make([]string, 0, len(b.Values))
		for k := range b.Values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			raw, _ := json.Marshal(b.Values[k])
			if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, string(raw)); err != nil {
				return audit.Event{}, err
			}
		}
		return s.rec(r.Context(), tx, r, p, "settings.updated", "settings", "server", map[string]any{"keys": keys, "values": b.Values})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("flag.changed", map[string]any{"key": "settings"})
	m, _ := s.settingsMap(r)
	writeJSON(w, 200, map[string]any{"values": m})
}

// ---------------- SSE ----------------

// ssePayload mirrors the mock: the event data, extended with server_now (except metrics.sample).
func ssePayload(ev events.Event) map[string]any {
	m, _ := ev.Data.(map[string]any)
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	if ev.Name != "metrics.sample" {
		out["server_now"] = now()
	}
	return out
}

func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "internal", "streaming unsupported", nil)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	writeSSE := func(ev events.Event) {
		b, _ := json.Marshal(ssePayload(ev))
		fmt.Fprintf(w, "event: %s\nid: %s\ndata: %s\n\n", ev.Name, ev.ID, b)
		fl.Flush()
	}
	for _, ev := range s.Bus.Since(r.URL.Query().Get("last_event_id")) {
		writeSSE(ev)
	}
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()
	ch, cancel := s.Bus.Subscribe()
	defer cancel()
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			writeSSE(ev)
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
