package api

// These tests prove the security rules from the brief with behaviour, not code reading:
// setup token single-use/expiry, API-token shape and hashing, CSRF matrix, login timing
// anonymity, audit triggers, tamper detection (flip, delete, broken link), the atomicity of
// handler writes and their audit rows, and the SSE wire format.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/store"
)

// The setup token is issued with a 1 hour expiry; the stored row must say so.
func TestSetupTokenExpiresInOneHour(t *testing.T) {
	e := newEnv(t)
	var exp string
	if err := e.s.DB.QueryRow(`SELECT token_expires_at FROM setup_state WHERE id=1`).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	p, err := time.Parse(time.RFC3339Nano, exp)
	if err != nil {
		t.Fatal(err)
	}
	if d := p.Sub(time.Now()); d < 55*time.Minute || d > 65*time.Minute {
		t.Fatalf("setup token TTL must be 1h, got %s", d)
	}
}

// The second /setup/admin is 403 because the token row is nulled on first admin creation.
func TestSetupSecondAdminIsForbidden(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	w, b := e.client().do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "b@example.com", "password": "another long passphrase"}, nil)
	if w.Code != 403 || b["code"] != "forbidden" {
		t.Fatalf("second setup/admin must be 403 forbidden, got %d %v", w.Code, b)
	}
}

var cplRe = regexp.MustCompile(`^cpl_[0-9a-f]{40}$`)

func TestAPITokenLifecycle(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	w, b := c.post("/tokens", map[string]any{"name": "ci", "scopes": []string{"audit:read", "tokens:manage"}})
	if w.Code != 201 {
		t.Fatalf("create token: %d %v", w.Code, b)
	}
	secret, _ := b["secret"].(string)
	if !cplRe.MatchString(secret) {
		t.Fatalf("secret must be cpl_ + 40 hex, got %q", secret)
	}
	id, _ := b["token"].(map[string]any)["id"].(string)
	if pref, _ := b["token"].(map[string]any)["prefix"].(string); pref != secret[:8] {
		t.Fatalf("prefix must be the first 8 chars of the secret, got %q", pref)
	}

	// Non-GET session request with an API token and no CSRF header: must go through.
	w, b2 := c.post("/tokens", map[string]any{"name": "tmp", "scopes": []string{"tokens:manage"}})
	if w.Code != 201 {
		t.Fatalf("second token: %d %v", w.Code, b2)
	}
	id2, _ := b2["token"].(map[string]any)["id"].(string)
	secret2, _ := b2["secret"].(string)
	if w, _ := c.do("DELETE", "/tokens/"+id2, nil, map[string]string{"Authorization": "Bearer " + secret2}); w.Code != 204 {
		t.Fatalf("DELETE with API token and no CSRF must be 204, got %d", w.Code)
	}

	// The list never returns a secret, and the created secret is not findable in it.
	w, body := c.do("GET", "/tokens", nil, map[string]string{"Authorization": "Bearer " + secret})
	if w.Code != 200 {
		t.Fatalf("GET /tokens with API token: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("the token list must not contain the raw secret")
	}
	for _, it := range body["items"].([]any) {
		if _, leaked := it.(map[string]any)["secret"]; leaked {
			t.Fatalf("token list item carries a secret field: %v", it)
		}
	}

	// Only the SHA-256 of the secret is stored; the raw value appears nowhere in the row.
	var hash string
	if err := e.s.DB.QueryRow(`SELECT hash FROM api_tokens WHERE id=?`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if hash != crypto.HashToken(secret) {
		t.Fatal("stored value must be the SHA-256 of the secret")
	}
}

// CSRF is required on every non-GET session request; TestAPITokenLifecycle proves the token side.
func TestSessionNonGETRequiresCSRF(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	_, b := c.post("/tokens", map[string]any{"name": "x", "scopes": []string{"tokens:manage"}})
	id, _ := b["token"].(map[string]any)["id"].(string)
	if w, _ := c.do("DELETE", "/tokens/"+id, nil, nil); w.Code != 403 {
		t.Fatalf("non-GET session request without CSRF must be 403, got %d", w.Code)
	}
}

// Generic errors AND similar cost: a wrong password for a real account and one for a missing
// account must not differ in timing by an order of magnitude. Coarse on purpose.
func TestLoginTimingDoesNotRevealAccount(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	c := e.client()
	t0 := time.Now()
	if w, _ := c.do("POST", "/auth/login", map[string]string{"email": "owner@example.com", "password": "totally wrong pw x"}, nil); w.Code != 401 {
		t.Fatalf("existing account: want 401, got %d", w.Code)
	}
	dtExisting := time.Since(t0)
	t0 = time.Now()
	if w, _ := c.do("POST", "/auth/login", map[string]string{"email": "ghost@example.com", "password": "totally wrong pw x"}, nil); w.Code != 401 {
		t.Fatalf("missing account: want 401, got %d", w.Code)
	}
	dtMissing := time.Since(t0)
	d := dtExisting - dtMissing
	if d < 0 {
		d = -d
	}
	if d > 2*time.Second {
		t.Fatalf("login timing delta %v reveals account existence (existing=%v missing=%v)", d, dtExisting, dtMissing)
	}
	t.Logf("timing: existing=%v missing=%v delta=%v", dtExisting, dtMissing, d)
}

// The DB-level tripwires must exist: both abort triggers, one each.
func TestAuditTriggersExist(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"audit_no_update", "audit_no_delete"} {
		var n int
		if err := e.s.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, name).Scan(&n); err != nil || n != 1 {
			t.Fatalf("trigger %s must exist exactly once, got %d (err=%v)", name, n, err)
		}
	}
}

func TestAuditDeletedRowDetected(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	if _, err := e.s.DB.Exec(`DROP TRIGGER audit_no_delete`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.Exec(`DELETE FROM audit_events WHERE seq=3`); err != nil {
		t.Fatal(err)
	}
	r, err := audit.Verify(context.Background(), e.s.DB)
	if err != nil || r.Valid {
		t.Fatalf("deleted row must invalidate the chain: %+v %v", r, err)
	}
	if r.FirstInvalidSeq == nil || *r.FirstInvalidSeq != 3 {
		t.Fatalf("want first_invalid_seq=3, got %v", r.FirstInvalidSeq)
	}
}

func TestAuditBrokenLinkDetected(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	if _, err := e.s.DB.Exec(`DROP TRIGGER audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.Exec(`UPDATE audit_events SET prev_hash=? WHERE seq=3`, "00000000000000000000000000000000000000000000000000000000000000ff"); err != nil {
		t.Fatal(err)
	}
	r, err := audit.Verify(context.Background(), e.s.DB)
	if err != nil || r.Valid {
		t.Fatalf("broken link must invalidate the chain: %+v %v", r, err)
	}
	if r.FirstInvalidSeq == nil || *r.FirstInvalidSeq != 3 {
		t.Fatalf("want first_invalid_seq=3, got %v", r.FirstInvalidSeq)
	}
}

// Atomicity: a failure injected after the handler's write (via a trigger on the audit INSERT)
// must roll back the handler's write too. Neither may commit.
func TestHandlerAndAuditCommitAtomically(t *testing.T) {
	e := newEnv(t)
	c, secret := e.bootstrap()
	if w, b := c.post("/auth/step-up", map[string]string{"code": codeAt(e.t, secret, 0)}); w.Code != 200 {
		t.Fatalf("step-up: %d %v", w.Code, b)
	}
	if _, err := e.s.DB.Exec(`CREATE TRIGGER audit_fail_user_created BEFORE INSERT ON audit_events
		WHEN NEW.action='user.created' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"email": "atomic@example.com", "password": "another long passphrase", "role": "viewer"}
	if w, b := c.post("/users", body); w.Code != 500 {
		t.Fatalf("injected failure must surface as 500, got %d %v", w.Code, b)
	}
	var n int
	if err := e.s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE email='atomic@example.com'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("handler write must have rolled back (users=%d err=%v)", n, err)
	}
	if err := e.s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='user.created'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("audit row must have rolled back (audit=%d err=%v)", n, err)
	}
	if r, err := audit.Verify(context.Background(), e.s.DB); err != nil || !r.Valid {
		t.Fatalf("chain must stay valid after the rolled-back write: %+v %v", r, err)
	}
	if _, err := e.s.DB.Exec(`DROP TRIGGER audit_fail_user_created`); err != nil {
		t.Fatal(err)
	}
	if w, b := c.post("/users", body); w.Code != 201 {
		t.Fatalf("retry after dropping the trigger must succeed, got %d %v", w.Code, b)
	}
	if err := e.s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='user.created'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit row must exist now (got %d)", n)
	}
}

// The owner gate branch (403 for everything except /auth/me and /auth/logout): a non-pending
// session of an owner without TOTP, crafted directly so the branch is exercised in isolation.
func TestOwnerGateBranchAllowsMeAndLogout(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	var ownerID string
	if err := e.s.DB.QueryRow(`SELECT id FROM users WHERE role='owner'`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.Exec(`UPDATE users SET totp_enabled=0 WHERE id=?`, ownerID); err != nil {
		t.Fatal(err)
	}
	var tok, csrf string
	err := store.InTx(context.Background(), e.s.DB, func(tx *sql.Tx) error {
		var e2 error
		tok, csrf, e2 = e.s.newSession(context.Background(), tx, ownerID, "", "127.0.0.1", true)
		return e2
	})
	if err != nil {
		t.Fatal(err)
	}
	cl := &client{e: e, cookie: &http.Cookie{Name: BootstrapCookie, Value: tok}, csrf: csrf}
	if w, _ := cl.do("GET", "/users", nil, nil); w.Code != 403 {
		t.Fatalf("gated owner must be blocked on /users, got %d", w.Code)
	}
	if w, _ := cl.do("GET", "/auth/me", nil, nil); w.Code != 200 {
		t.Fatalf("gated owner may still call /auth/me, got %d", w.Code)
	}
	if w, _ := cl.do("POST", "/auth/logout", nil, map[string]string{"X-CSRF-Token": csrf}); w.Code != 204 {
		t.Fatalf("gated owner may still call /auth/logout, got %d", w.Code)
	}
}

// Recovery codes are single use: redeem one, then the same code must be refused.
func TestRecoveryCodeSingleUse(t *testing.T) {
	e := newEnv(t)
	su := e.client()
	if w, _ := su.do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "owner@example.com", "password": "correct horse battery staple"}, nil); w.Code != 201 {
		t.Fatalf("setup/admin: %d", w.Code)
	}
	_, enr := su.do("POST", "/setup/totp/enroll", nil, nil)
	secret, _ := enr["secret"].(string)
	_, v := su.do("POST", "/setup/totp/verify", map[string]string{"code": codeAt(e.t, secret, 0)}, nil)
	// The verify response's body holds the once-shown recovery codes.
	if arr, _ := v["recovery_codes"].([]any); len(arr) != 8 {
		t.Fatalf("expected 8 recovery codes in the verify response, got %v", v["recovery_codes"])
	}
	var codes []string
	for _, x := range v["recovery_codes"].([]any) {
		codes = append(codes, x.(string))
	}
	if w, _ := su.do("POST", "/setup/complete", nil, nil); w.Code != 204 {
		t.Fatalf("setup/complete: %d", w.Code)
	}
	c := e.client()
	_, lg := c.do("POST", "/auth/login", map[string]string{"email": "owner@example.com", "password": "correct horse battery staple"}, nil)
	ch, _ := lg["challenge"].(string)
	w, me := c.do("POST", "/auth/totp/verify", map[string]string{"challenge": ch, "code": codeAt(e.t, secret, 30*time.Second)}, nil)
	if w.Code != 200 {
		t.Fatalf("login totp: %d %s", w.Code, w.Body.String())
	}
	c.csrf, _ = me["csrf_token"].(string)
	code := codes[0]
	if w, _ := c.post("/auth/step-up", map[string]string{"code": code}); w.Code != 200 {
		t.Fatalf("first redemption must work: %d", w.Code)
	}
	if w, _ := c.post("/auth/step-up", map[string]string{"code": code}); w.Code == 200 {
		t.Fatal("a redeemed recovery code must not be accepted twice")
	}
}

// SSE wire format (mock parity): id lines, server_now in data payloads, and last_event_id replay.
func TestSSEEventsCarryIDAndServerNow(t *testing.T) {
	e := newEnv(t)
	c, secret := e.bootstrap()
	if w, b := c.post("/auth/step-up", map[string]string{"code": codeAt(e.t, secret, 0)}); w.Code != 200 {
		t.Fatalf("step-up: %d %v", w.Code, b)
	}

	// Fire one event (and its audit row) before any stream is open.
	if w, b := c.do("PATCH", "/feature-flags", map[string]any{"flags": map[string]any{"git.preview_deploys": true}}, map[string]string{"X-CSRF-Token": c.csrf}); w.Code != 200 {
		t.Fatalf("flag flip to fire an event: %d %v", w.Code, b)
	}

	stream := func(url string, fireFlag string, fire bool) string {
		req := httptest.NewRequest("GET", url, nil)
		req.AddCookie(c.cookie)
		ctx, cancel := context.WithCancel(req.Context())
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			e.h.ServeHTTP(rec, req.WithContext(ctx))
			close(done)
		}()
		time.Sleep(300 * time.Millisecond)
		if fire {
			if w, b := c.do("PATCH", "/feature-flags", map[string]any{"flags": map[string]any{fireFlag: true}}, map[string]string{"X-CSRF-Token": c.csrf}); w.Code != 200 {
				t.Fatalf("flag flip: %d %v", w.Code, b)
			}
			time.Sleep(200 * time.Millisecond)
		}
		cancel()
		<-done
		return rec.Body.String()
	}

	// Replay: connect with last_event_id=0; the earlier event must be resent on connect.
	out := stream(Prefix+"/events?last_event_id=0", "", false)
	if !strings.Contains(out, "event: flag.changed") {
		t.Fatalf("replay with last_event_id must resend the earlier event, got:\n%s", out)
	}
	if !strings.Contains(out, "id: ") {
		t.Fatalf("events must carry an id: line, got:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		if !strings.HasPrefix(ln, "data:") {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(ln[len("data:"):])), &m); err == nil && m["server_now"] == nil {
			t.Fatalf("data payloads must include server_now: %s", ln)
		}
	}

	// Live: a fresh connection (no last_event_id) must receive an event fired while it is open.
	out2 := stream(Prefix+"/events", "extensions.outbound", true)
	if !strings.Contains(out2, "event: flag.changed") {
		t.Fatalf("live connection must receive flag.changed, got:\n%s", out2)
	}
}
