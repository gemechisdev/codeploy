package api

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

const (
	SecureCookie    = "__Host-codeplo"
	BootstrapCookie = "codeplo_session" // loopback-only, non-__Host- variant (Safari drops Secure cookies on http://localhost)
	idleTimeout     = 2 * time.Hour
)

type User struct {
	ID, Email, Role       string
	TOTPEnabled, Disabled bool
	CreatedAt             string
	LastLoginAt           *string
	PasswordHash          string
	TOTPSecretEnc         []byte
}

func (u User) Public() map[string]any {
	return map[string]any{"id": u.ID, "email": u.Email, "role": u.Role, "totp_enabled": u.TOTPEnabled, "disabled": u.Disabled, "created_at": u.CreatedAt, "last_login_at": u.LastLoginAt}
}

type Principal struct {
	User        User
	SessionID   string // sha256 of the opaque token (empty for API tokens)
	CSRF        string
	StepUpUntil time.Time
	Bootstrap   bool
	Pending     string // non-empty while a login challenge or setup session is unfinished
	Token       bool
	Perms       map[string]bool
}

func (p *Principal) PermList() []string {
	out := make([]string, 0, len(p.Perms))
	for k := range p.Perms {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func isSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	k, _ := r.Context().Value(listenerKey{}).(string)
	return k == "unix" && r.Header.Get("X-Forwarded-Proto") == "https"
}

// setSessionCookie issues the opaque token; HTTPS gets __Host-codeplo, the loopback listener the marked bootstrap variant.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) (bootstrap bool) {
	secure := isSecure(r)
	name := BootstrapCookie
	if secure {
		name = SecureCookie
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds())})
	return !secure
}

func clearSessionCookie(w http.ResponseWriter) {
	for _, n := range []string{SecureCookie, BootstrapCookie} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: n == SecureCookie})
	}
}

func (s *Server) settingInt(ctx context.Context, key string, def int) int {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return def
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (s *Server) sessionTTL(ctx context.Context) time.Duration {
	return time.Duration(s.settingInt(ctx, "session_ttl_h", 12)) * time.Hour
}

// newSession stores sha256(token) and returns the raw token (shown only in the Set-Cookie header).
func (s *Server) newSession(ctx context.Context, tx *sql.Tx, userID, pending, ip string, bootstrap bool) (token, csrf string, err error) {
	token, csrf = crypto.RandomHex(32), crypto.RandomHex(16)
	n := time.Now()
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(id,user_id,csrf,created_at,last_seen_at,expires_at,bootstrap,pending_challenge,ip) VALUES (?,?,?,?,?,?,?,?,?)`,
		crypto.HashToken(token), userID, csrf, iso(n), iso(n), iso(n.Add(s.sessionTTL(ctx))), b2i(bootstrap), nullStr(pending), ip)
	return
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) rolePerms(ctx context.Context, role string) map[string]bool {
	out := map[string]bool{}
	rows, err := s.DB.QueryContext(ctx, `SELECT permission FROM role_permissions WHERE role=?`, role)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil {
			out[p] = true
		}
	}
	return out
}

const userCols = `u.id,u.email,u.role,u.totp_enabled,u.disabled,u.created_at,u.last_login_at,u.password_hash,u.totp_secret_enc`

func scanUser(sc interface{ Scan(...any) error }) (User, error) {
	var u User
	var te, d int
	err := sc.Scan(&u.ID, &u.Email, &u.Role, &te, &d, &u.CreatedAt, &u.LastLoginAt, &u.PasswordHash, &u.TOTPSecretEnc)
	u.TOTPEnabled, u.Disabled = te == 1, d == 1
	return u, err
}

// authenticate resolves a session cookie or `Authorization: Bearer cpl_…` token. Returns (nil,nil) when absent/invalid.
func (s *Server) authenticate(r *http.Request) (*Principal, error) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer cpl_") {
		return s.authToken(ctx, strings.TrimPrefix(h, "Bearer "))
	}
	var tok string
	for _, n := range []string{SecureCookie, BootstrapCookie} {
		if c, err := r.Cookie(n); err == nil && c.Value != "" {
			tok = c.Value
			break
		}
	}
	if tok == "" {
		return nil, nil
	}
	id := crypto.HashToken(tok)
	var uid, csrf, seen, exp string
	var stepUp, pending sql.NullString
	var boot int
	err := s.DB.QueryRowContext(ctx, `SELECT user_id,csrf,last_seen_at,expires_at,step_up_until,bootstrap,pending_challenge FROM sessions WHERE id=?`, id).
		Scan(&uid, &csrf, &seen, &exp, &stepUp, &boot, &pending)
	if err != nil {
		return nil, nil
	}
	ts, _ := time.Parse(time.RFC3339Nano, seen)
	te, _ := time.Parse(time.RFC3339Nano, exp)
	if time.Now().After(te) || time.Since(ts) > idleTimeout {
		_, _ = s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id=?`, id)
		return nil, nil
	}
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id=?`, uid))
	if err != nil || u.Disabled {
		return nil, nil
	}
	if time.Since(ts) > time.Minute {
		// Sliding absolute expiry: active sessions keep living up to session_ttl_h from now,
		// so a long-open tab is not kicked after a fixed wall-clock window.
		n := time.Now()
		_, _ = s.DB.ExecContext(ctx, `UPDATE sessions SET last_seen_at=?, expires_at=? WHERE id=?`,
			iso(n), iso(n.Add(s.sessionTTL(ctx))), id)
	}
	p := &Principal{User: u, SessionID: id, CSRF: csrf, Bootstrap: boot == 1, Pending: pending.String, Perms: s.rolePerms(ctx, u.Role)}
	if stepUp.Valid {
		p.StepUpUntil, _ = time.Parse(time.RFC3339Nano, stepUp.String)
	}
	return p, nil
}

func (s *Server) authToken(ctx context.Context, raw string) (*Principal, error) {
	var id, scopes, uid string
	var exp sql.NullString
	if err := s.DB.QueryRowContext(ctx, `SELECT id,scopes,user_id,expires_at FROM api_tokens WHERE hash=?`, crypto.HashToken(raw)).Scan(&id, &scopes, &uid, &exp); err != nil {
		return nil, nil
	}
	if exp.Valid {
		if t, err := time.Parse(time.RFC3339Nano, exp.String); err == nil && time.Now().After(t) {
			return nil, nil
		}
	}
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id=?`, uid))
	if err != nil || u.Disabled {
		return nil, nil
	}
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, now(), id)
	owner := s.rolePerms(ctx, u.Role)
	perms := map[string]bool{}
	for _, sc := range strings.Split(scopes, ",") {
		if owner[sc] { // a token never exceeds its owner's current role
			perms[sc] = true
		}
	}
	return &Principal{User: u, Token: true, Perms: perms}, nil
}

// requireStepUp returns false (after writing 403 step_up_required) unless a TOTP re-check is still valid.
func requireStepUp(w http.ResponseWriter, p *Principal) bool {
	if p.StepUpUntil.After(time.Now()) {
		return true
	}
	writeErr(w, 403, "step_up_required", "Re-enter your 2FA code to continue", nil)
	return false
}
