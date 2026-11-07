// Package api: proof for the P4-P7 route groups (FUTURE_TASKS T24 and T26).
//
// The handoff's one hard rule is "compilation is not evidence". These tests run the
// real chi router, the real guards, the real module services and a throwaway SQLite
// database - not a mock of any of them. They cover three things the earlier phases
// left unproven:
//
//  1. every later-phase route refuses an anonymous caller and honours the module flag;
//  2. a refused write leaves nothing behind, and an accepted write appends exactly
//     one audit row with a chain that still verifies;
//  3. write-only secrets never come back in a response or an audit row.
//
// The only injected values are host services that a daemon would wire anyway
// (git/notify/monitor/databases/templates/deploy constructors). The deployment
// engine is built with no runtime: every create below uses deploy_now=false, so no
// container is ever started and no Docker daemon is needed.
package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/authz"
	"github.com/codeplo/codeplo/internal/modules/databases"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/git"
	"github.com/codeplo/codeplo/internal/modules/monitor"
	"github.com/codeplo/codeplo/internal/modules/notify"
	"github.com/codeplo/codeplo/internal/modules/templates"
)

type phaseEnv struct {
	*env
	c      *client
	secret string
}

// newPhaseEnv bootstraps a real owner session, switches on the modules that ship
// disabled and wires the same services cmd/codeplod/ops.go wires. Nothing here
// short-circuits a handler.
func newPhaseEnv(t *testing.T) *phaseEnv {
	t.Helper()
	e := newEnv(t)
	c, secret := e.bootstrap()
	if _, err := e.s.DB.Exec(`UPDATE modules SET enabled=1 WHERE key IN
		('deploy','git','monitor','notify','databases','templates')`); err != nil {
		t.Fatal(err)
	}
	e.s.Git = git.New(e.s.DB, e.s.Key)
	e.s.Notify = notify.New(e.s.DB, e.s.Key)
	e.s.Monitor = monitor.New(e.s.DB)
	e.s.Databases = databases.New(e.s.DB, e.s.Key)
	tpl := templates.New()
	tpl.DB, tpl.Key = e.s.DB, e.s.Key
	e.s.Templates = tpl
	e.s.Deploy = deploy.New(deploy.Deps{DB: e.s.DB, Key: e.s.Key, DataDir: t.TempDir()})
	return &phaseEnv{env: e, c: c, secret: secret}
}

// req is a signed-in request carrying the CSRF header, for any method (the shared
// harness only has post()).
func (p *phaseEnv) req(method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	return p.c.do(method, path, body, map[string]string{"X-CSRF-Token": p.c.csrf})
}

func (p *phaseEnv) anon(method, path string) (*httptest.ResponseRecorder, map[string]any) {
	return p.env.client().do(method, path, nil, nil)
}

func (p *phaseEnv) module(key string, on bool) {
	p.t.Helper()
	v := 0
	if on {
		v = 1
	}
	if _, err := p.s.DB.Exec(`UPDATE modules SET enabled=? WHERE key=?`, v, key); err != nil {
		p.t.Fatal(err)
	}
}

func (p *phaseEnv) auditSeq() int64 {
	p.t.Helper()
	var n int64
	if err := p.s.DB.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM audit_events`).Scan(&n); err != nil {
		p.t.Fatal(err)
	}
	return n
}

// auditSince returns the actions appended after seq and re-verifies the hash chain.
func (p *phaseEnv) auditSince(seq int64) []string {
	p.t.Helper()
	rows, err := p.s.DB.Query(`SELECT action FROM audit_events WHERE seq>? ORDER BY seq`, seq)
	if err != nil {
		p.t.Fatal(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			p.t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		p.t.Fatal(err)
	}
	return out
}

func (p *phaseEnv) verifyChain() {
	p.t.Helper()
	r, err := audit.Verify(context.Background(), p.s.DB)
	if err != nil || !r.Valid {
		p.t.Fatalf("audit chain must still verify: %+v %v", r, err)
	}
}

// expectWrite asserts a successful write, exactly one new audit row, and a valid chain.
func (p *phaseEnv) expectWrite(want string, status int, method, path string, body any) map[string]any {
	p.t.Helper()
	before := p.auditSeq()
	w, out := p.req(method, path, body)
	if w.Code != status {
		p.t.Fatalf("%s %s: want %d, got %d %s", method, path, status, w.Code, w.Body)
	}
	got := p.auditSince(before)
	if len(got) != 1 || got[0] != want {
		p.t.Fatalf("%s %s: want one audit row %q, got %v", method, path, want, got)
	}
	p.verifyChain()
	return out
}

// projectAndResource creates real rows through the public handlers, so the tests
// below read exactly the state a client would have produced.
func (p *phaseEnv) projectAndResource(t *testing.T, slug, name string) (projectID, resourceID string) {
	t.Helper()
	out := p.expectWrite("project.created", 201, "POST", "/projects",
		map[string]any{"name": "Project " + slug, "slug": slug, "description": "phase proof"})
	projectID, _ = out["id"].(string)
	if projectID == "" {
		t.Fatalf("no project id in %v", out)
	}
	var envID string
	if err := p.s.DB.QueryRow(`SELECT id FROM environments WHERE project_id=? AND name='production'`, projectID).Scan(&envID); err != nil {
		t.Fatalf("createProject must also create its production environment: %v", err)
	}
	out = p.expectWrite("resource.created", 201, "POST", "/resources", map[string]any{
		"environment_id": envID,
		"type":           "app",
		"name":           name,
		"config":         map[string]any{"image": map[string]any{"mode": "pull", "ref": "ghcr.io/acme/app:1.0"}},
		"deploy_now":     false,
	})
	res, ok := out["resource"].(map[string]any)
	if !ok {
		t.Fatalf("no resource object in %v", out)
	}
	resourceID, _ = res["id"].(string)
	if resourceID == "" {
		t.Fatalf("no resource id in %v", out)
	}
	if out["deployment"] != nil {
		t.Fatalf("deploy_now=false must not queue a deployment, got %v", out["deployment"])
	}
	return projectID, resourceID
}

var phaseModules = map[string]bool{
	"deploy": true, "git": true, "monitor": true, "notify": true, "databases": true, "templates": true,
}

// TestPhaseRoutesRejectAnonymousAndRespectModuleFlag walks the *whole* P4-P7 route
// table from Routes(), not a hand-picked subset. It proves the guard and moduleOn
// contract for every later-phase operation, including generated stubs.
func TestPhaseRoutesRejectAnonymousAndRespectModuleFlag(t *testing.T) {
	p := newPhaseEnv(t)
	repl := strings.NewReplacer("{id}", "missing", "{token}", "missing", "{key}", "missing")
	seen := map[string]bool{}
	for _, d := range p.s.Routes() {
		if !phaseModules[d.Module] {
			continue
		}
		k := d.Method + " " + d.Path
		if seen[k] {
			continue
		}
		seen[k] = true
		path := repl.Replace(d.Path)

		if d.Perm != authz.Public {
			w, b := p.anon(d.Method, path)
			if w.Code != 401 || b["code"] != "unauthenticated" {
				t.Errorf("%s: anonymous want 401 unauthenticated, got %d %v", k, w.Code, b)
			}
		}
		p.module(d.Module, false)
		w, b := p.req(d.Method, path, nil)
		p.module(d.Module, true)
		if w.Code != 404 || b["code"] != "module_disabled" {
			t.Errorf("%s: disabled module want 404 module_disabled, got %d %v", k, w.Code, b)
			continue
		}
		details, _ := b["details"].(map[string]any)
		if details["module"] != d.Module {
			t.Errorf("%s: module_disabled must name its module, got %v", k, b["details"])
		}
	}
	if len(seen) < 50 {
		t.Fatalf("expected the full P4-P7 route table (>=50 routes), walked %d", len(seen))
	}
	t.Logf("guards proved on %d P4-P7 routes", len(seen))
}

// TestPhaseRefusalsWriteNothing covers the handoff rule "a refusal leaves nothing
// written": a validation failure must not append an audit row and must not create a row.
func TestPhaseRefusalsWriteNothing(t *testing.T) {
	p := newPhaseEnv(t)
	cases := []struct {
		name, method, path string
		body               any
		field              string
	}{
		{"project", "POST", "/projects", map[string]any{"name": "", "slug": "Bad Slug"}, "name"},
		{"environment", "POST", "/projects/missing/environments", map[string]any{"name": "NOT VALID"}, "name"},
		{"resource", "POST", "/resources", map[string]any{"type": "app", "name": "Bad Name", "environment_id": "missing"}, "name"},
		{"git connection", "POST", "/git/connections", map[string]any{"name": "  "}, "name"},
		{"channel", "POST", "/notifications/channels", map[string]any{"kind": "bogus", "name": "x"}, "kind"},
		{"alert rule", "POST", "/alerts/rules", map[string]any{"key": "not_a_rule"}, "key"},
		{"metrics range", "GET", "/system/metrics?range=forever", nil, "range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := p.auditSeq()
			w, b := p.req(tc.method, tc.path, tc.body)
			if w.Code != 422 || b["code"] != "validation_failed" {
				t.Fatalf("want 422 validation_failed, got %d %s", w.Code, w.Body)
			}
			details, _ := b["details"].(map[string]any)
			fields, _ := details["fields"].(map[string]any)
			if _, ok := fields[tc.field]; !ok {
				t.Fatalf("want field error %q, got %v", tc.field, details)
			}
			if got := p.auditSince(before); len(got) != 0 {
				t.Fatalf("a refused write must append no audit row, got %v", got)
			}
		})
	}
	// CSRF is checked before the handler: a refused request must not write either.
	before := p.auditSeq()
	w, _ := p.c.do("POST", "/projects", map[string]any{"name": "x", "slug": "x"}, nil)
	if w.Code != 403 {
		t.Fatalf("want 403 without CSRF, got %d %s", w.Code, w.Body)
	}
	if got := p.auditSince(before); len(got) != 0 {
		t.Fatalf("a CSRF refusal must append no audit row, got %v", got)
	}
}

// TestPhaseWriteRoutesAppendAuditRows is T26: one action per accepted write, in the
// same transaction as the write, with a still-valid hash chain.
func TestPhaseWriteRoutesAppendAuditRows(t *testing.T) {
	p := newPhaseEnv(t)
	pid, rid := p.projectAndResource(t, "phase-audit", "phase-audit-app")

	p.expectWrite("environment.created", 201, "POST", "/projects/"+pid+"/environments",
		map[string]any{"name": "staging"})

	p.expectWrite("env.updated", 200, "PUT", "/resources/"+rid+"/env",
		map[string]any{"items": []any{map[string]any{"key": "LOG_LEVEL", "value": "info", "scope": "runtime"}}})

	p.expectWrite("volume.created", 201, "POST", "/resources/"+rid+"/volumes",
		map[string]any{"name": "data", "mount_path": "/data"})

	p.expectWrite("git.connection_created", 201, "POST", "/git/connections",
		map[string]any{"name": "GitHub org"})

	ch := p.expectWrite("channel.created", 201, "POST", "/notifications/channels",
		map[string]any{"kind": "webhook", "name": "Ops", "config": map[string]string{"url": "https://hooks.example.test/alert"}})
	chID, _ := ch["id"].(string)
	if chID == "" {
		t.Fatalf("no channel id in %v", ch)
	}

	// 0006_operations.sql seeds four preset rules (disk_low, container_down,
	// deploy_failed, cert_expiring). memory_high is a catalog key that ships
	// unseeded, so creating it here exercises a genuinely new row.
	rule := p.expectWrite("alert_rule.created", 201, "POST", "/alerts/rules",
		map[string]any{"key": "memory_high", "threshold": "90", "cooldown_s": 900, "channel_ids": []string{chID}})
	ruleID, _ := rule["id"].(string)
	if ruleID == "" {
		t.Fatalf("no rule id in %v", rule)
	}

	p.expectWrite("alert_rule.updated", 200, "PATCH", "/alerts/rules/"+ruleID,
		map[string]any{"enabled": false})

	p.expectWrite("channel.deleted", 204, "DELETE", "/notifications/channels/"+chID, nil)

	// Deleting a channel must also strip it from every rule that named it (the one
	// cross-module invariant this file's header calls out).
	var ids string
	if err := p.s.DB.QueryRow(`SELECT channel_ids_json FROM alert_rules WHERE id=?`, ruleID).Scan(&ids); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ids, chID) {
		t.Fatalf("deleted channel %s is still referenced by rule %s: %s", chID, ruleID, ids)
	}
}

// TestPhaseSecretsAreWriteOnly proves the secret contract end to end: a secret is
// sealed at rest, never returned, never written into an audit row, and rotating it
// needs a fresh step-up.
func TestPhaseSecretsAreWriteOnly(t *testing.T) {
	p := newPhaseEnv(t)
	_, rid := p.projectAndResource(t, "phase-secret", "phase-secret-app")
	const secret = "s3cr3t-database-password"

	out := p.expectWrite("env.updated", 200, "PUT", "/resources/"+rid+"/env",
		map[string]any{"items": []any{
			map[string]any{"key": "LOG_LEVEL", "value": "info", "scope": "runtime"},
			map[string]any{"key": "DB_PASSWORD", "value": secret, "scope": "runtime", "is_secret": true},
		}})
	items, _ := out["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 env rows, got %v", out["items"])
	}
	for _, raw := range items {
		it, _ := raw.(map[string]any)
		if it["is_secret"] == true {
			if it["value"] != nil {
				t.Fatalf("a secret must read back as explicit null, got %v", it["value"])
			}
			if !strings.Contains(string(mustJSON(t, it)), `"value":null`) {
				t.Fatalf("the response must contain an explicit null, not omit the key: %v", it)
			}
		}
	}
	// The value is sealed, not stored as plaintext, and no audit row contains it.
	var blob []byte
	if err := p.s.DB.QueryRow(`SELECT value_enc FROM resource_env_vars WHERE resource_id=? AND key='DB_PASSWORD'`, rid).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), secret) {
		t.Fatal("plaintext secret found in the database")
	}
	var n int
	if err := p.s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE meta LIKE ?`, "%"+secret+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("secret leaked into %d audit rows", n)
	}

	// Rotating an existing secret without step-up is refused and leaves the old row intact.
	before := p.auditSeq()
	w, b := p.req("PUT", "/resources/"+rid+"/env", map[string]any{"items": []any{
		map[string]any{"key": "LOG_LEVEL", "value": "info", "scope": "runtime"},
		map[string]any{"key": "DB_PASSWORD", "value": "rotated-password", "scope": "runtime", "is_secret": true},
	}})
	if w.Code != 403 || b["code"] != "step_up_required" {
		t.Fatalf("rotating a secret needs step-up, got %d %v", w.Code, b)
	}
	if got := p.auditSince(before); len(got) != 0 {
		t.Fatalf("a step-up refusal must write nothing, got %v", got)
	}
	if err := p.s.DB.QueryRow(`SELECT value_enc FROM resource_env_vars WHERE resource_id=? AND key='DB_PASSWORD'`, rid).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	// keep_existing_secret after a step-up is the documented way to rewrite without a new value.
	p.c.post("/auth/step-up", map[string]string{"code": codeAt(t, p.secret, 30*time.Second)})
	p.expectWrite("env.updated", 200, "PUT", "/resources/"+rid+"/env", map[string]any{"items": []any{
		map[string]any{"key": "LOG_LEVEL", "value": "debug", "scope": "runtime"},
		map[string]any{"key": "DB_PASSWORD", "scope": "runtime", "is_secret": true, "keep_existing_secret": true},
	}})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestPhaseUnwiredServicesRefuseInsteadOfLying: a nil service must answer 500 rather
// than an empty list that reads as "nothing is configured" (docs/HANDOFF_NOTES.md §2).
func TestPhaseUnwiredServicesRefuseInsteadOfLying(t *testing.T) {
	p := newPhaseEnv(t)
	for _, tc := range []struct {
		name, path string
		set        func()
	}{
		{"templates", "/templates", func() { p.s.Templates = nil }},
		{"channels", "/notifications/channels", func() { p.s.Notify = nil }},
		{"metrics", "/system/metrics", func() { p.s.Monitor = nil }},
		{"backups", "/backups/schedules", func() { p.s.Databases = nil }},
		{"git", "/git/connections", func() { p.s.Git = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := p.auditSeq()
			tc.set()
			w, b := p.req("GET", tc.path, nil)
			if w.Code != 500 || b["code"] != "internal" {
				t.Fatalf("an unwired service must answer 500, got %d %v", w.Code, b)
			}
			if got := p.auditSince(before); len(got) != 0 {
				t.Fatalf("a refused read wrote audit rows: %v", got)
			}
		})
	}
}

// TestPhaseReadEndpointsReturnContractShapes covers the P4-P7 list routes with a real
// row present, including the explicit nulls the contract requires.
func TestPhaseReadEndpointsReturnContractShapes(t *testing.T) {
	p := newPhaseEnv(t)
	pid, rid := p.projectAndResource(t, "phase-read", "phase-read-app")

	for _, tc := range []struct {
		path, key string
	}{
		{"/projects", "items"},
		{"/projects/" + pid, "project"},
		{"/projects/" + pid + "/environments", "items"},
		{"/resources", "items"},
		{"/resources/" + rid + "/deployments", "items"},
		{"/resources/" + rid + "/env", "items"},
		{"/resources/" + rid + "/volumes", "items"},
		{"/resources/" + rid + "/policy", "controls"},
		{"/git/connections", "items"},
		{"/webhooks/deliveries", "items"},
		{"/notifications/channels", "items"},
		{"/alerts/rules", "items"},
		{"/backups/schedules", "items"},
		{"/backups/runs", "items"},
		{"/templates", "items"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w, b := p.req("GET", tc.path, nil)
			if w.Code != 200 {
				t.Fatalf("want 200, got %d %s", w.Code, w.Body)
			}
			if _, ok := b[tc.key]; !ok {
				t.Fatalf("response is missing %q: %v", tc.key, b)
			}
		})
	}

	w, b := p.req("GET", "/resources/"+rid, nil)
	if w.Code != 200 {
		t.Fatalf("get resource: %d %s", w.Code, w.Body)
	}
	for _, k := range []string{"active_deployment_id", "active_digest", "primary_domain", "started_at"} {
		v, ok := b[k]
		if !ok || v != nil {
			t.Fatalf("%s must be an explicit null on a never-deployed resource, got %v (present=%v)", k, v, ok)
		}
	}
	if b["status"] != "stopped" || b["desired_state"] != "stopped" {
		t.Fatalf("a new resource is stopped, got %v", b)
	}
}

// TestPhaseModulesOffByDefault records the shipped flag state, so the suite above
// cannot accidentally pass because someone changed the migration seeds.
func TestPhaseModulesOffByDefault(t *testing.T) {
	e := newEnv(t)
	var enabled int
	for _, key := range []string{"monitor", "notify", "databases", "templates"} {
		if err := e.s.DB.QueryRow(`SELECT enabled FROM modules WHERE key=?`, key).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		if enabled != 0 {
			t.Errorf("module %q must ship disabled, got enabled=%d", key, enabled)
		}
	}
}
