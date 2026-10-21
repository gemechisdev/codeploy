package api

import (
	"context"
	"database/sql"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/codeplo/codeplo/internal/modules/edge"
)

// seedEdgeResources creates two real resources for the domain routes. Since P4 a domain can only be attached to a
// resource that exists (404 otherwise), so these fixtures hold rows rather than bare ids.
func seedEdgeResources(t *testing.T, db *sql.DB) {
	t.Helper()
	cfg := `{"image":{"mode":"pull","ref":"nginx:latest"},"port":8080,"healthcheck":{"path":"/","interval_s":10,` +
		`"retries":3,"start_period_s":10,"timeout_s":5},"limits":{"memory_mb":256,"cpus":0.5,"pids_limit":256},` +
		`"isolation":{"cap_add":[],"trusted":false},"restart":"unless-stopped","retain_images":5}`
	must := func(q string, args ...any) {
		if _, err := db.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	must(`INSERT INTO projects(id,name,slug,created_at) VALUES ('prj_edge','Edge','edge',?)`, now())
	must(`INSERT INTO environments(id,project_id,name,created_at) VALUES ('env_edge','prj_edge','production',?)`, now())
	for _, id := range []string{"res_1", "res_2"} {
		must(`INSERT INTO resources(id,environment_id,type,name,slug,config_json,desired_state,created_at)
			VALUES (?,?, 'app',?,?,?, 'stopped',?)`, id, "env_edge", id, strings.ToLower(id), cfg, now())
	}
}

// fakeCaddy is a stub of the Caddy admin API served over a unix socket, so edge tests never require real Caddy.
// It records how many times POST /load happened so a read-only path can be proven not to mutate the edge.
type fakeCaddy struct {
	mu    sync.Mutex
	cfg   []byte
	loads int
	srv   *http.Server
}

func (f *fakeCaddy) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loads
}

func startFakeCaddy(t *testing.T) (*fakeCaddy, string) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "caddy-admin.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable on this host: %v", err)
	}
	f := &fakeCaddy{cfg: []byte(`{"apps":{}}`)}
	mux := http.NewServeMux()
	mux.HandleFunc("/config/", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(f.cfg)
	})
	mux.HandleFunc("/load", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.cfg = b
		f.loads++
		f.mu.Unlock()
		w.WriteHeader(200)
	})
	f.srv = &http.Server{Handler: mux}
	go func() { _ = f.srv.Serve(ln) }()
	t.Cleanup(func() { _ = f.srv.Close() })
	return f, socket
}

// wireEdge attaches a reconciler to the server backed by the admin socket at `socket`.
func (e *env) wireEdge(socket string) {
	e.s.Edge = &edge.Reconciler{
		DB:      e.s.DB,
		Client:  edge.NewClient(socket),
		Opts:    edge.Options{AdminSocket: socket, PanelSocket: filepath.Join(e.t.TempDir(), "panel.sock")},
		DataDir: e.t.TempDir(),
		Unit:    "codeplo-caddy",
	}
}

func TestEdgeStatusIsReadOnly(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	seedEdgeResources(t, e.s.DB)
	// Create a domain before wiring the reconciler so no background reconcile can POST /load.
	if w, _ := c.post("/resources/res_1/domains", map[string]any{"hostname": "shop.example.com"}); w.Code != 201 {
		t.Fatalf("create domain: %d", w.Code)
	}
	f, socket := startFakeCaddy(t)
	e.wireEdge(socket)
	w, st := c.do("GET", "/edge/status", nil, nil)
	if w.Code != 200 {
		t.Fatalf("edge/status: %d %s", w.Code, w.Body)
	}
	if st["caddy_running"] != true {
		t.Fatalf("want caddy_running true, got %v", st["caddy_running"])
	}
	if h, ok := st["config_hash"].(string); !ok || h == "" {
		t.Fatalf("want a config hash, got %v", st["config_hash"])
	}
	c.do("GET", "/edge/status", nil, nil) // repeated reads must still not load
	if n := f.loadCount(); n != 0 {
		t.Fatalf("status must never load config into Caddy, loads=%d", n)
	}
}

func TestEdgeReconcileSuccessAndCaddyDown(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	seedEdgeResources(t, e.s.DB)
	// Create the domain before wiring so no background reconcile touches the stub counts.
	if w, _ := c.post("/resources/res_1/domains", map[string]any{"hostname": "shop.example.com"}); w.Code != 201 {
		t.Fatalf("create domain: %d", w.Code)
	}
	f, socket := startFakeCaddy(t)
	e.wireEdge(socket)
	w, st := c.post("/edge/reconcile", nil)
	if w.Code != 200 {
		t.Fatalf("reconcile: %d %s", w.Code, w.Body)
	}
	if st["caddy_running"] != true || st["drift"] != false {
		t.Fatalf("after reconcile want caddy_running true and drift false, got %v", st)
	}
	if f.loadCount() == 0 {
		t.Fatal("reconcile must load the config into Caddy")
	}

	// A reconciler pointed at a dead socket cannot reach Caddy: the contract maps that to 409 conflict.
	e.wireEdge(filepath.Join(t.TempDir(), "does-not-exist.sock"))
	w, b := c.post("/edge/reconcile", nil)
	if w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("want 409 conflict when Caddy is down, got %d %v", w.Code, b)
	}
}

func TestDomainsCRUDConflictAndAudit(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	seedEdgeResources(t, e.s.DB)

	w, d := c.post("/resources/res_1/domains", map[string]any{"hostname": "App.Example.com"})
	if w.Code != 201 {
		t.Fatalf("create domain: %d %s", w.Code, w.Body)
	}
	id, _ := d["id"].(string)
	if d["hostname"] != "app.example.com" || d["is_panel"] != false || d["cert_status"] != "issuing" {
		t.Fatalf("unexpected domain: %v", d)
	}
	if d["resource_id"] != "res_1" {
		t.Fatalf("want resource_id res_1, got %v", d["resource_id"])
	}

	// The hostname is normalised and stored once; a second attempt conflicts.
	if w, b := c.post("/resources/res_1/domains", map[string]any{"hostname": "app.example.com"}); w.Code != 409 || b["code"] != "conflict" {
		t.Fatalf("duplicate hostname must be 409 conflict, got %d %v", w.Code, b)
	}

	// A hostname already used by the panel domain is refused as a resource domain (422, not 409).
	if _, err := e.s.DB.Exec(`INSERT INTO domains(id,hostname,kind,force_https,redirect_www,cert_status,created_at) VALUES ('dom_panel','panel.example.com','panel',1,0,'issuing',?)`, now()); err != nil {
		t.Fatal(err)
	}
	w, b := c.post("/resources/res_2/domains", map[string]any{"hostname": "panel.example.com"})
	if w.Code != 422 {
		t.Fatalf("reserved panel hostname must be 422, got %d %v", w.Code, b)
	}
	det, _ := b["details"].(map[string]any)
	fld, _ := det["fields"].(map[string]any)
	if msg, _ := fld["hostname"].(string); !strings.Contains(msg, "reserved for the panel") {
		t.Fatalf("want a reserved-for-panel message, got %v", fld)
	}

	// Invalid hostname format is a 422 before any transaction runs.
	if w, _ := c.post("/resources/res_1/domains", map[string]any{"hostname": "not a host"}); w.Code != 422 {
		t.Fatalf("bad hostname must be 422, got %d", w.Code)
	}

	// The successful create appended exactly one hash-chained audit row in its own transaction.
	var n int
	if err := e.s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action='domain.created'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want one domain.created audit row from the committed insert, got %d", n)
	}

	// Listing works globally and per resource.
	if w, p := c.do("GET", "/domains", nil, nil); w.Code != 200 || len(p["items"].([]any)) < 2 {
		t.Fatalf("list domains: %d %v", w.Code, p)
	}
	w, rp := c.do("GET", "/resources/res_1/domains", nil, nil)
	if w.Code != 200 {
		t.Fatalf("list resource domains: %d", w.Code)
	}
	items, _ := rp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("want one res_1 domain, got %v", items)
	}

	// Delete removes it; the panel domain is not addressable through the resource path (404).
	if w, _ := c.do("DELETE", "/resources/res_1/domains/"+id, nil, map[string]string{"X-CSRF-Token": c.csrf}); w.Code != 204 {
		t.Fatalf("delete domain: %d %s", w.Code, w.Body)
	}
	if w, _ := c.do("DELETE", "/resources/res_2/domains/dom_panel", nil, map[string]string{"X-CSRF-Token": c.csrf}); w.Code != 404 {
		t.Fatalf("deleting the panel domain via a resource path must be 404, got %d", w.Code)
	}
}

func TestSetupPanelDomainKeepsOnePanel(t *testing.T) {
	e := newEnv(t)
	su := e.client()
	if w, _ := su.do("POST", "/setup/admin", map[string]string{"token": e.tok, "email": "owner@example.com", "password": "correct horse battery staple"}, nil); w.Code != 200 && w.Code != 201 {
		t.Fatalf("setup/admin: %d %s", w.Code, w.Body)
	}
	w, b := su.do("POST", "/setup/panel-domain", map[string]string{"hostname": "a.panel.test"}, nil)
	if w.Code != 200 || b["cert_status"] != "issuing" {
		t.Fatalf("panel-domain: %d %v", w.Code, b)
	}
	if b["url"] != nil {
		t.Fatalf("url must be null until the certificate is issued, got %v", b["url"])
	}
	if w, _ := su.do("POST", "/setup/panel-domain", map[string]string{"hostname": "b.panel.test"}, nil); w.Code != 200 {
		t.Fatalf("second panel-domain: %d", w.Code)
	}
	var n int
	var latest string
	if err := e.s.DB.QueryRow(`SELECT COUNT(*), MAX(hostname) FROM domains WHERE kind='panel'`).Scan(&n, &latest); err != nil {
		t.Fatal(err)
	}
	if n != 1 || latest != "b.panel.test" {
		t.Fatalf("want exactly one panel domain b.panel.test, got %d %s", n, latest)
	}
}
