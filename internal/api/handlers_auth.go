package api

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/authn"
	"github.com/codeplo/codeplo/internal/authz"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/platform"
	"github.com/codeplo/codeplo/internal/store"
)

var (
	emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	hostRe  = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)
)

// rec appends an audit row in the caller's tx and returns the event for publication after commit.
func (s *Server) rec(ctx context.Context, tx *sql.Tx, r *http.Request, p *Principal, action, tt, tid string, meta map[string]any) (audit.Event, error) {
	e := audit.Entry{ActorType: "user", ActorID: "anonymous", IP: clientIP(r), Action: action, TargetType: tt, TargetID: tid, Meta: meta}
	if p != nil {
		e.ActorID, e.ActorEmail = p.User.ID, p.User.Email
		if p.Token {
			e.ActorType = "token"
		}
	}
	return audit.Append(ctx, tx, e)
}

func (s *Server) published(ev audit.Event) {
	s.Bus.Publish("audit.appended", map[string]any{"seq": ev.Seq})
}

// tx runs fn and its audit write atomically.
func (s *Server) tx(r *http.Request, fn func(tx *sql.Tx) (audit.Event, error)) error {
	var ev audit.Event
	err := store.InTx(r.Context(), s.DB, func(tx *sql.Tx) (e error) { ev, e = fn(tx); return })
	if err == nil && ev.Seq > 0 {
		s.published(ev)
	}
	return err
}

func (s *Server) coreRoutes() []RouteDef {
	P, S := authz.Public, authz.Session
	return []RouteDef{
		{Method: "GET", Path: "/setup/state", Perm: P, Handler: s.setupState},
		{Method: "POST", Path: "/setup/admin", Perm: P, Handler: s.setupAdmin, NoCSRF: true},
		{Method: "POST", Path: "/setup/totp/enroll", Perm: P, Handler: s.setupEnroll, NoCSRF: true},
		{Method: "POST", Path: "/setup/totp/verify", Perm: P, Handler: s.setupVerify, NoCSRF: true},
		{Method: "POST", Path: "/setup/verify-domain", Perm: P, Handler: s.setupVerifyDomain, NoCSRF: true},
		{Method: "POST", Path: "/setup/panel-domain", Perm: P, Handler: s.setupPanelDomain, NoCSRF: true},
		{Method: "GET", Path: "/setup/operator-candidates", Perm: P, Handler: s.setupOperatorCandidates, NoCSRF: true},
		{Method: "POST", Path: "/setup/operator", Perm: P, Handler: s.setupOperator, NoCSRF: true},
		{Method: "POST", Path: "/setup/complete", Perm: P, Handler: s.setupComplete, NoCSRF: true},
		{Method: "POST", Path: "/auth/login", Perm: P, Handler: s.login},
		{Method: "POST", Path: "/auth/totp/verify", Perm: P, Handler: s.totpVerify},
		{Method: "GET", Path: "/auth/me", Perm: S, Handler: s.me},
		{Method: "POST", Path: "/auth/step-up", Perm: S, Handler: s.stepUp},
		{Method: "POST", Path: "/auth/logout", Perm: S, Handler: s.logout},
		{Method: "GET", Path: "/users", Perm: "users:manage", Handler: s.listUsers},
		{Method: "POST", Path: "/users", Perm: "users:manage", Handler: s.createUser},
		{Method: "PATCH", Path: "/users/{id}", Perm: "users:manage", Handler: s.patchUser},
		{Method: "GET", Path: "/tokens", Perm: "tokens:manage", Handler: s.listTokens},
		{Method: "POST", Path: "/tokens", Perm: "tokens:manage", Handler: s.createToken},
		{Method: "DELETE", Path: "/tokens/{id}", Perm: "tokens:manage", Handler: s.deleteToken},
		{Method: "GET", Path: "/roles", Perm: S, Handler: s.roles},
		{Method: "GET", Path: "/permissions", Perm: S, Handler: s.permissions},
		{Method: "GET", Path: "/audit", Perm: "audit:read", Handler: s.listAudit},
		{Method: "POST", Path: "/audit/verify", Perm: "audit:read", Handler: s.verifyAudit},
		{Method: "GET", Path: "/system/status", Perm: "system:read", Handler: s.systemStatus},
		{Method: "GET", Path: "/system/version", Perm: "system:read", Handler: s.systemVersion},
		{Method: "GET", Path: "/modules", Perm: S, Handler: s.listModules},
		{Method: "PATCH", Path: "/modules/{key}", Perm: "modules:manage", Handler: s.patchModule},
		{Method: "GET", Path: "/feature-flags", Perm: S, Handler: s.listFlags},
		{Method: "PATCH", Path: "/feature-flags", Perm: "flags:manage", Handler: s.patchFlags},
		{Method: "GET", Path: "/settings", Perm: S, Handler: s.getSettings},
		{Method: "PATCH", Path: "/settings", Perm: "flags:manage", Handler: s.patchSettings},
		{Method: "GET", Path: "/events", Perm: S, Handler: s.sse},
	}
}

// ---------------- setup ----------------

type setupRow struct {
	TokenHash, TokenExp, AdminID, Operator sql.NullString
	Step                                   string
	TOTPEnc                                []byte
}

func (s *Server) setupRow(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (setupRow, error) {
	var x setupRow
	err := q.QueryRowContext(ctx, `SELECT token_hash,token_expires_at,admin_id,operator,step,totp_secret_enc FROM setup_state WHERE id=1`).
		Scan(&x.TokenHash, &x.TokenExp, &x.AdminID, &x.Operator, &x.Step, &x.TOTPEnc)
	return x, err
}

func (s *Server) setupState(w http.ResponseWriter, r *http.Request) {
	x, err := s.setupRow(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"setup_required": x.Step != "done", "token_valid": x.AdminID.Valid, "step": x.Step, "has_operator": x.Operator.String != ""})
}

// setupSession: after /setup/admin the wizard is bound to a setup-scoped session cookie (pending == "setup").
func (s *Server) setupSession(w http.ResponseWriter, r *http.Request) (*Principal, setupRow, bool) {
	x, err := s.setupRow(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return nil, x, false
	}
	if x.Step == "done" {
		writeErr(w, 404, "not_found", "Setup is disabled: an owner already exists", nil)
		return nil, x, false
	}
	p := from(r)
	if p == nil || p.Pending != "setup" || !x.AdminID.Valid || p.User.ID != x.AdminID.String {
		writeErr(w, 401, "unauthenticated", "Setup session missing. Start again with the setup token.", nil)
		return nil, x, false
	}
	return p, x, true
}

func (s *Server) setupAdmin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Token, Email, Password string }
	if !decode(w, r, &b) {
		return
	}
	x, err := s.setupRow(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	if x.Step == "done" {
		writeErr(w, 404, "not_found", "Setup is disabled: an owner already exists", nil)
		return
	}
	exp, _ := time.Parse(time.RFC3339Nano, x.TokenExp.String)
	if !x.TokenHash.Valid || b.Token == "" || time.Now().After(exp) || !crypto.Equal(x.TokenHash.String, crypto.HashToken(b.Token)) {
		writeErr(w, 403, "forbidden", "The one-time setup token is invalid or expired", nil)
		return
	}
	f := map[string]string{}
	if !emailRe.MatchString(b.Email) {
		f["email"] = "Enter a valid email address"
	}
	if len(b.Password) < authn.MinPasswordLen {
		f["password"] = "Use at least 12 characters"
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
	u := User{ID: newID("usr_"), Email: b.Email, Role: "owner", CreatedAt: now()}
	var tok string
	var conflict bool
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return audit.Event{}, err
		}
		if n > 0 {
			conflict = true
			return audit.Event{}, sql.ErrNoRows
		}
		if _, err := tx.Exec(`INSERT INTO users(id,email,password_hash,role,created_at) VALUES (?,?,?,?,?)`, u.ID, u.Email, hash, u.Role, u.CreatedAt); err != nil {
			return audit.Event{}, err
		}
		// Token is single-use: it expires on first admin creation.
		if _, err := tx.Exec(`UPDATE setup_state SET admin_id=?, step='totp', token_hash=NULL, token_expires_at=NULL WHERE id=1`, u.ID); err != nil {
			return audit.Event{}, err
		}
		var e error
		tok, _, e = s.newSession(r.Context(), tx, u.ID, "setup", clientIP(r), !isSecure(r))
		if e != nil {
			return audit.Event{}, e
		}
		return s.rec(r.Context(), tx, r, &Principal{User: u}, "setup.admin_created", "user", u.ID, nil)
	})
	if conflict {
		writeErr(w, 409, "conflict", "Owner already created", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	setSessionCookie(w, r, tok, time.Hour)
	writeJSON(w, 201, u.Public())
}

func (s *Server) setupEnroll(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.setupSession(w, r)
	if !ok {
		return
	}
	secret, uri, err := authn.NewTOTP(p.User.Email)
	if err != nil {
		internalErr(w, err)
		return
	}
	enc, err := crypto.Seal(s.Key, []byte(secret))
	if err == nil {
		_, err = s.DB.ExecContext(r.Context(), `UPDATE setup_state SET totp_secret_enc=? WHERE id=1`, enc)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"secret": secret, "otpauth_uri": uri})
}

func (s *Server) setupVerify(w http.ResponseWriter, r *http.Request) {
	p, x, ok := s.setupSession(w, r)
	if !ok {
		return
	}
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	if len(x.TOTPEnc) == 0 {
		writeErr(w, 409, "conflict", "Start TOTP enrollment first", nil)
		return
	}
	secret, err := crypto.Open(s.Key, x.TOTPEnc)
	if err != nil {
		internalErr(w, err)
		return
	}
	if !authn.ValidTOTP(string(secret), b.Code) || !s.replay.Fresh("setup", b.Code) {
		invalid(w, map[string]string{"code": "That code is not valid. Check your authenticator and try again."})
		return
	}
	codes, hashes := authn.NewRecoveryCodes(8)
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.Exec(`UPDATE users SET totp_secret_enc=?, totp_enabled=1 WHERE id=?`, x.TOTPEnc, p.User.ID); err != nil {
			return audit.Event{}, err
		}
		for _, h := range hashes {
			if _, err := tx.Exec(`INSERT INTO recovery_codes(user_id,hash) VALUES (?,?)`, p.User.ID, h); err != nil {
				return audit.Event{}, err
			}
		}
		if _, err := tx.Exec(`UPDATE setup_state SET step='domain' WHERE id=1`); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "setup.totp_enrolled", "user", p.User.ID, nil)
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"recovery_codes": codes}) // shown once; only hashes are stored
}

func (s *Server) setupVerifyDomain(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.setupSession(w, r); !ok {
		return
	}
	var b struct{ Hostname string }
	if !decode(w, r, &b) {
		return
	}
	h := strings.ToLower(strings.TrimSpace(b.Hostname))
	if len(h) > 253 || !hostRe.MatchString(h) {
		invalid(w, map[string]string{"hostname": "Enter a valid hostname such as panel.example.com"})
		return
	}
	hostIPs := []string{}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.IsGlobalUnicast() && !ipn.IP.IsPrivate() {
				hostIPs = append(hostIPs, ipn.IP.String())
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, h)
	resolved := []string{}
	match := false
	for _, ip := range ips {
		resolved = append(resolved, ip.IP.String())
		for _, hi := range hostIPs {
			if hi == ip.IP.String() {
				match = true
			}
		}
	}
	detail := "Resolves to this host."
	switch {
	case err != nil || len(resolved) == 0:
		detail = "No A/AAAA record found yet. DNS changes can take a while to propagate."
	case !match:
		detail = "The name resolves to " + strings.Join(resolved, ", ") + ", which is not this host."
	}
	writeJSON(w, 200, map[string]any{"hostname": h, "resolves": len(resolved) > 0, "resolved_ips": resolved, "host_ips": hostIPs, "matches_host": match, "detail": detail})
}

func (s *Server) setupOperatorCandidates(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.setupSession(w, r); !ok {
		return
	}
	writeJSON(w, 200, map[string]any{"items": platform.OperatorCandidates()})
}

// setupOperator records an existing sudo user as the operator. Creating a user needs root and arrives with the
// P2 hardening helper, so mode "create" is refused with a contract error instead of being faked.
func (s *Server) setupOperator(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.setupSession(w, r)
	if !ok {
		return
	}
	var b struct{ Mode, Username, PublicKey string }
	if !decode(w, r, &b) {
		return
	}
	if !platform.UsernameRe.MatchString(b.Username) {
		invalid(w, map[string]string{"username": "Lowercase letters, digits, \"-\" and \"_\" (max 32)"})
		return
	}
	if b.Mode == "create" {
		writeErr(w, 501, "module_disabled", "Creating an operator user needs the host-hardening helper (P2). Choose an existing user.", map[string]any{"implemented": false})
		return
	}
	var cand *platform.Operator
	for _, c := range platform.OperatorCandidates() {
		if c.Username == b.Username {
			c := c
			cand = &c
		}
	}
	if cand == nil || !cand.Sudo {
		invalid(w, map[string]string{"username": "Choose a user with sudo access and an authorized_keys file"})
		return
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.Exec(`UPDATE setup_state SET operator=?, step='audit' WHERE id=1`, cand.Username); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "setup.operator_set", "user", p.User.ID, map[string]any{"operator": cand.Username})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, cand)
}

func (s *Server) setupComplete(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.setupSession(w, r)
	if !ok {
		return
	}
	if !p.User.TOTPEnabled {
		writeErr(w, 409, "conflict", "The owner must enroll TOTP before setup can complete", nil)
		return
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.Exec(`UPDATE sessions SET pending_challenge=NULL, expires_at=? WHERE id=?`, iso(time.Now().Add(s.sessionTTL(r.Context()))), p.SessionID); err != nil {
			return audit.Event{}, err
		}
		if _, err := tx.Exec(`UPDATE setup_state SET step='done' WHERE id=1`); err != nil {
			return audit.Event{}, err
		}
		if _, err := tx.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, now(), p.User.ID); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "setup.completed", "system", "setup", nil)
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// ---------------- auth ----------------

func (s *Server) meBody(r *http.Request, p *Principal) map[string]any {
	var su any
	if p.StepUpUntil.After(time.Now()) {
		su = iso(p.StepUpUntil)
	}
	return map[string]any{"user": p.User.Public(), "role": p.User.Role, "permissions": p.PermList(), "step_up_until": su,
		"csrf_token": p.CSRF, "server_now": now(), "bootstrap": p.Bootstrap}
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var b struct{ Email, Password string }
	if !decode(w, r, &b) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(b.Email))
	if ok, wait := s.lim.allow("acct:"+email, 5, 1.0/6); !ok { // per-account bucket
		w.Header().Set("Retry-After", itoa(wait))
		writeErr(w, 429, "rate_limited", "Too many failed attempts. Try again shortly.", nil)
		return
	}
	u, err := scanUser(s.DB.QueryRowContext(r.Context(), `SELECT `+userCols+` FROM users u WHERE u.email=?`, email))
	good := err == nil && !u.Disabled && authn.VerifyPassword(b.Password, u.PasswordHash)
	if err != nil {
		authn.DummyVerify()
	}
	if !good {
		tid := "unknown"
		if err == nil {
			tid = u.ID
		}
		_ = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
			return s.rec(r.Context(), tx, r, nil, "auth.login_failed", "user", tid, nil)
		})
		writeErr(w, 401, "unauthenticated", "Invalid email or password", nil) // generic: no user enumeration
		return
	}
	pending, challenge := "", ""
	if u.TOTPEnabled {
		challenge = newID("chl_")
		pending = challenge
	}
	var tok string
	err = s.tx(r, func(tx *sql.Tx) (ev audit.Event, e error) {
		tok, _, e = s.newSession(r.Context(), tx, u.ID, pending, clientIP(r), !isSecure(r))
		if e != nil || u.TOTPEnabled {
			return
		}
		_, _ = tx.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, now(), u.ID)
		return s.rec(r.Context(), tx, r, &Principal{User: u}, "auth.login", "user", u.ID, nil)
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	setSessionCookie(w, r, tok, s.sessionTTL(r.Context()))
	if u.TOTPEnabled {
		writeJSON(w, 200, map[string]any{"totp_required": true, "challenge": challenge})
		return
	}
	writeJSON(w, 200, map[string]any{"totp_required": false})
}

func itoa(n int) string { return strconv.Itoa(n) }

// checkSecondFactor accepts a current TOTP code or a single-use recovery code (consumed inside tx when given).
func (s *Server) checkSecondFactor(ctx context.Context, tx *sql.Tx, u User, code string) bool {
	if len(u.TOTPSecretEnc) > 0 {
		if sec, err := crypto.Open(s.Key, u.TOTPSecretEnc); err == nil && authn.ValidTOTP(string(sec), code) && s.replay.Fresh(u.ID, code) {
			return true
		}
	}
	if tx != nil && strings.Contains(code, "-") {
		res, err := tx.ExecContext(ctx, `UPDATE recovery_codes SET used_at=? WHERE user_id=? AND hash=? AND used_at IS NULL`, now(), u.ID, crypto.HashToken(authn.NormalizeRecovery(code)))
		if err == nil {
			n, _ := res.RowsAffected()
			return n == 1
		}
	}
	return false
}

func (s *Server) totpVerify(w http.ResponseWriter, r *http.Request) {
	var b struct{ Challenge, Code string }
	if !decode(w, r, &b) {
		return
	}
	p := from(r)
	if p == nil || p.Pending == "" || p.Pending == "setup" || !crypto.Equal(p.Pending, b.Challenge) {
		writeErr(w, 401, "unauthenticated", "Login challenge expired. Sign in again.", nil)
		return
	}
	if ok, wait := s.lim.allow("totp:"+p.User.ID, 5, 1.0/6); !ok {
		w.Header().Set("Retry-After", itoa(wait))
		writeErr(w, 429, "rate_limited", "Too many attempts. Try again shortly.", nil)
		return
	}
	var tok, csrf string
	bad := false
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if !s.checkSecondFactor(r.Context(), tx, p.User, b.Code) {
			bad = true
			return audit.Event{}, sql.ErrNoRows
		}
		// Rotate the session id on privilege change (anti-fixation).
		if _, err := tx.Exec(`DELETE FROM sessions WHERE id=?`, p.SessionID); err != nil {
			return audit.Event{}, err
		}
		var e error
		tok, csrf, e = s.newSession(r.Context(), tx, p.User.ID, "", clientIP(r), !isSecure(r))
		if e != nil {
			return audit.Event{}, e
		}
		_, _ = tx.Exec(`UPDATE users SET last_login_at=? WHERE id=?`, now(), p.User.ID)
		return s.rec(r.Context(), tx, r, p, "auth.login", "user", p.User.ID, nil)
	})
	if bad {
		invalid(w, map[string]string{"code": "That code is not valid."})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	boot := setSessionCookie(w, r, tok, s.sessionTTL(r.Context()))
	np := &Principal{User: p.User, CSRF: csrf, Bootstrap: boot, Perms: p.Perms}
	writeJSON(w, 200, s.meBody(r, np))
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.meBody(r, from(r)))
}

func (s *Server) stepUp(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	var b struct{ Code string }
	if !decode(w, r, &b) {
		return
	}
	if p.Token {
		writeErr(w, 403, "forbidden", "API tokens cannot perform step-up", nil)
		return
	}
	if ok, wait := s.lim.allow("totp:"+p.User.ID, 5, 1.0/6); !ok {
		w.Header().Set("Retry-After", itoa(wait))
		writeErr(w, 429, "rate_limited", "Too many attempts. Try again shortly.", nil)
		return
	}
	until := time.Now().Add(time.Duration(s.settingInt(r.Context(), "step_up_window_s", 300)) * time.Second)
	bad := false
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if !s.checkSecondFactor(r.Context(), tx, p.User, b.Code) {
			bad = true
			return audit.Event{}, sql.ErrNoRows
		}
		if _, err := tx.Exec(`UPDATE sessions SET step_up_until=? WHERE id=?`, iso(until), p.SessionID); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "auth.step_up", "user", p.User.ID, nil)
	})
	if bad {
		invalid(w, map[string]string{"code": "That code is not valid."})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("session.step_up", map[string]any{"step_up_until": iso(until)})
	writeJSON(w, 200, map[string]any{"step_up_until": iso(until)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	p := from(r)
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if p.SessionID != "" {
			if _, err := tx.Exec(`DELETE FROM sessions WHERE id=?`, p.SessionID); err != nil {
				return audit.Event{}, err
			}
		}
		return s.rec(r.Context(), tx, r, p, "auth.logout", "user", p.User.ID, nil)
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(204)
}
