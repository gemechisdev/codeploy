package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"

	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/store"
)

// These tests run the real router against a throwaway SQLite file (t.TempDir, WAL). A file is used
// instead of ":memory:" because the pool opens several connections and each would see its own empty database.

type env struct {
	t   *testing.T
	s   *Server
	h   http.Handler
	tok string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	key := []byte(crypto.RandomHex(16)) // 32 bytes
	s := New(db, key, "test", "test", "test")
	tok, err := app.IssueSetupToken(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, s: s, h: s.Handler("loopback"), tok: tok}
}

type client struct {
	e      *env
	cookie *http.Cookie
	csrf   string
}

func (e *env) client() *client { return &client{e: e} }

func (c *client) do(method, path string, body any, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	c.e.t.Helper()
	var rd bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&rd).Encode(body)
	}
	req := httptest.NewRequest(method, Prefix+path, &rd)
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	c.e.h.ServeHTTP(w, req)
	for _, ck := range w.Result().Cookies() {
		if ck.Name == BootstrapCookie && ck.MaxAge > 0 {
			c.cookie = ck
		}
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

// post sends the CSRF header the way the frontend does.
func (c *client) post(path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	return c.do("POST", path, body, map[string]string{"X-CSRF-Token": c.csrf})
}

func codeAt(t *testing.T, secret string, off time.Duration) string {
	t.Helper()
	c, err := totp.GenerateCodeCustom(secret, time.Now().Add(off), totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// bootstrap runs setup (admin, TOTP enroll and verify) and then a full login. It returns the TOTP secret.
func (e *env) bootstrap() (*client, string) {
	t := e.t
	t.Helper()
	su := e.client()
	if w, _ := su.do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "owner@example.com", "password": "correct horse battery staple"}, nil); w.Code != 200 && w.Code != 201 {
		t.Fatalf("setup/admin: %d %s", w.Code, w.Body)
	}
	_, enr := su.do("POST", "/setup/totp/enroll", nil, nil)
	secret, _ := enr["secret"].(string)
	if secret == "" {
		t.Fatalf("no secret in enroll response: %v", enr)
	}
	if w, _ := su.do("POST", "/setup/totp/verify", map[string]string{"code": codeAt(t, secret, 0)}, nil); w.Code != 200 {
		t.Fatalf("setup/totp/verify: %d %s", w.Code, w.Body)
	}
	c := e.client()
	_, lg := c.do("POST", "/auth/login", map[string]string{"email": "owner@example.com", "password": "correct horse battery staple"}, nil)
	ch, _ := lg["challenge"].(string)
	w, me := c.do("POST", "/auth/totp/verify", map[string]string{"challenge": ch, "code": codeAt(t, secret, 30*time.Second)}, nil)
	if w.Code != 200 {
		t.Fatalf("totp verify: %d %s", w.Code, w.Body)
	}
	c.csrf, _ = me["csrf_token"].(string)
	return c, secret
}

func TestSetupIsSingleUse(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if w, _ := c.do("POST", "/setup/admin", map[string]string{"token": "wrong", "email": "a@example.com", "password": "correct horse battery staple"}, nil); w.Code != 403 {
		t.Fatalf("wrong token must be 403, got %d", w.Code)
	}
	e.bootstrap()
	w, _ := e.client().do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "b@example.com", "password": "correct horse battery staple"}, nil)
	if w.Code == 200 || w.Code == 201 {
		t.Fatalf("second setup must fail, got %d", w.Code)
	}
}

func TestSetupTokenExpires(t *testing.T) {
	e := newEnv(t)
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := e.s.DB.Exec(`UPDATE setup_state SET token_expires_at=? WHERE id=1`, past); err != nil {
		t.Fatal(err)
	}
	if w, _ := e.client().do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "a@example.com", "password": "correct horse battery staple"}, nil); w.Code != 403 {
		t.Fatalf("expired token must be 403, got %d", w.Code)
	}
}

func TestCSRFRejected(t *testing.T) {
	e := newEnv(t)
	c, secret := e.bootstrap()
	w, body := c.do("POST", "/auth/step-up", map[string]string{"code": codeAt(t, secret, -30*time.Second)}, nil) // no X-CSRF-Token
	if w.Code != 403 || body["code"] != "forbidden" {
		t.Fatalf("want 403 forbidden without CSRF header, got %d %v", w.Code, body)
	}
	if w, _ := c.do("POST", "/auth/step-up", map[string]string{"code": codeAt(t, secret, -30*time.Second)}, map[string]string{"X-CSRF-Token": "bogus"}); w.Code != 403 {
		t.Fatalf("want 403 with wrong CSRF header, got %d", w.Code)
	}
}

func TestStepUpRequiredThenExpires(t *testing.T) {
	e := newEnv(t)
	c, secret := e.bootstrap()
	newUser := map[string]string{"email": "dev@example.com", "password": "another long passphrase", "role": "deployer"}
	if w, b := c.post("/users", newUser); w.Code != 403 || b["code"] != "step_up_required" {
		t.Fatalf("want step_up_required, got %d %v", w.Code, b)
	}
	if w, b := c.post("/auth/step-up", map[string]string{"code": codeAt(t, secret, -30*time.Second)}); w.Code != 200 {
		t.Fatalf("step-up: %d %v", w.Code, b)
	}
	if w, b := c.post("/users", newUser); w.Code != 201 {
		t.Fatalf("create user after step-up: %d %v", w.Code, b)
	}
	// Expire the window; the same action must be refused again.
	if _, err := e.s.DB.Exec(`UPDATE sessions SET step_up_until=?`, time.Now().Add(-time.Second).UTC().Format("2006-01-02T15:04:05.000Z")); err != nil {
		t.Fatal(err)
	}
	newUser["email"] = "dev2@example.com"
	if w, b := c.post("/users", newUser); w.Code != 403 || b["code"] != "step_up_required" {
		t.Fatalf("step-up must expire, got %d %v", w.Code, b)
	}
}

func TestTOTPCodeCannotBeReplayed(t *testing.T) {
	e := newEnv(t)
	c, secret := e.bootstrap()
	code := codeAt(t, secret, -30*time.Second)
	if w, _ := c.post("/auth/step-up", map[string]string{"code": code}); w.Code != 200 {
		t.Fatalf("first use: %d", w.Code)
	}
	if w, _ := c.post("/auth/step-up", map[string]string{"code": code}); w.Code == 200 {
		t.Fatal("the same code must not be accepted twice")
	}
}

func TestOwnerGatedUntilTOTPEnrolled(t *testing.T) {
	e := newEnv(t)
	su := e.client()
	su.do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "owner@example.com", "password": "correct horse battery staple"}, nil)
	if w, _ := su.do("GET", "/users", nil, nil); w.Code != 403 && w.Code != 401 {
		t.Fatalf("owner without TOTP must not reach /users, got %d", w.Code)
	}
}

func TestAuditChainDetectsTamper(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	ctx := context.Background()
	r, err := audit.Verify(ctx, e.s.DB)
	if err != nil || !r.Valid || r.VerifiedRows < 3 {
		t.Fatalf("fresh chain must verify: %+v %v", r, err)
	}
	// Appends are refused by trigger; a tamper needs the attacker to drop it first (the point of tamper-EVIDENT).
	if _, err := e.s.DB.Exec(`UPDATE audit_events SET action='x' WHERE seq=2`); err == nil {
		t.Fatal("trigger must refuse UPDATE on audit_events")
	}
	if _, err := e.s.DB.Exec(`DROP TRIGGER audit_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DB.Exec(`UPDATE audit_events SET action='x' WHERE seq=2`); err != nil {
		t.Fatal(err)
	}
	r, err = audit.Verify(ctx, e.s.DB)
	if err != nil || r.Valid || r.FirstInvalidSeq == nil || *r.FirstInvalidSeq != 2 {
		t.Fatalf("want invalid with first_invalid_seq=2, got %+v %v", r, err)
	}
}

func TestAuthEndpointsAreRateLimited(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	var got *httptest.ResponseRecorder
	for i := 0; i < 60; i++ {
		w, _ := c.do("GET", "/setup/state", nil, nil)
		if w.Code == 429 {
			got = w
			break
		}
	}
	if got == nil || got.Header().Get("Retry-After") == "" {
		t.Fatal("expected 429 with Retry-After after a burst on /setup/*")
	}
}

func TestLoginErrorsAreGeneric(t *testing.T) {
	e := newEnv(t)
	e.bootstrap()
	c := e.client()
	_, a := c.do("POST", "/auth/login", map[string]string{"email": "owner@example.com", "password": "wrong password here"}, nil)
	_, b := c.do("POST", "/auth/login", map[string]string{"email": "nobody@example.com", "password": "wrong password here"}, nil)
	if a["code"] != b["code"] || a["message"] != b["message"] {
		t.Fatalf("login must not reveal whether an account exists: %v vs %v", a, b)
	}
}

func TestSetupOperatorValidation(t *testing.T) {
	e := newEnv(t)
	su := e.client()
	su.do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "owner@example.com", "password": "correct horse battery staple"}, nil)
	if w, _ := su.do("POST", "/setup/operator", map[string]string{"mode": "existing", "username": "Not Valid"}, nil); w.Code != 422 {
		t.Fatalf("bad username must be 422, got %d", w.Code)
	}
	// incomplete key is validation, not 501
	if w, b := su.do("POST", "/setup/operator", map[string]string{"mode": "create", "username": "deployer", "public_key": "not-a-key"}, nil); w.Code != 422 {
		t.Fatalf("bad public key must be 422, got %d %v", w.Code, b)
	}
	// create may succeed as root, or 403 when not root — never 501
	if w, b := su.do("POST", "/setup/operator", map[string]string{"mode": "create", "username": "ops-t27", "public_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITestKeyForCodeploT27ValidationOnly comment"}, nil); w.Code == 501 {
		t.Fatalf("create mode must not return 501, got %d %v", w.Code, b)
	} else if w.Code != 200 && w.Code != 403 && w.Code != 422 && w.Code != 500 {
		t.Fatalf("create mode unexpected status %d %v", w.Code, b)
	}
	if w, _ := e.client().do("GET", "/setup/operator-candidates", nil, nil); w.Code != 401 {
		t.Fatalf("candidates need the setup session, got %d", w.Code)
	}
}
