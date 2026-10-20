//go:build spike

// Spike C: the P2a edge renderer against a real Caddy. It proves three product claims that unit
// tests with a fake admin server cannot: the rendered JSON is accepted by Caddy (`validate` and a
// real `/load`), the panel site redirects to HTTPS while a plain resource site is proxied over HTTP,
// and pushing the same render twice reports no drift. TLS certificates are NOT exercised: a test
// hostname cannot issue a real ACME certificate, and the redirect path proves the routing decision.
// Run: go test -tags spike ./test/spikes -run CaddyEdge -v   (needs the `caddy` binary; skips otherwise)
package spikes

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/modules/edge"
)

// startOrigin runs a throwaway HTTP server on 127.0.0.1 and returns its host:port.
func startOrigin(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\norigin")
			}(c)
		}
	}()
	return ln.Addr().String()
}

func TestCaddyEdgeRenderLoadNoDrift(t *testing.T) {
	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Skip("caddy binary not available: spike NOT run (this is a skip, not a pass)")
	}
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// The renderer's own output, with a panel site and one plain resource site.
	origin := startOrigin(t)
	sites := []edge.Site{
		{Hostname: "panel.test.invalid", Panel: true, ForceHTTPS: true},
		{Hostname: "app.test.invalid", Upstream: origin, ForceHTTPS: false, RedirectWWW: true},
	}
	opts := edge.Options{AdminSocket: admin, PanelSocket: filepath.Join(dir, "panel.sock")}
	cfg, hash, err := edge.Render(sites, opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// 1. Caddy must accept the rendered config on its own.
	cf := filepath.Join(dir, "rendered.json")
	if err := os.WriteFile(cf, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(ctx, bin, "validate", "--config", cf).CombinedOutput(); err != nil {
		t.Fatalf("caddy validate rejected the rendered config: %v\n%s", err, out)
	}

	// Caddy starts from a bootstrap that only opens the admin socket; routes arrive by /load,
	// which is how the reconciler is designed to work (ADR 0003).
	bootstrap := fmt.Sprintf(`{"admin":{"listen":"unix/%s|0660","origins":["caddy"]}}`, admin)
	bf := filepath.Join(dir, "bootstrap.json")
	if err := os.WriteFile(bf, []byte(bootstrap), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "--config", bf)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	c := edge.NewClient(admin)
	healthy := false
	for i := 0; i < 100; i++ {
		if c.Healthy(ctx) {
			healthy = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("caddy admin socket never came up")
	}

	// 2. /load the rendered config. Caddy validates before swapping, so an error here means the
	// renderer produces a config Caddy refuses to run.
	if err := c.Load(ctx, cfg); err != nil {
		t.Fatalf("load rendered config: %v", err)
	}

	// 3. Behaviour over the plain HTTP listener: the panel host must redirect to HTTPS, a resource
	// host with force_https off must be proxied, and its www alias must be redirected to the apex.
	// The redirect is the assertion, so the client must not follow it: a test hostname cannot
	// resolve, and following the 308 would fail on DNS instead of proving the routing decision.
	noFollow := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	probeHost := func(host string) (int, string, string) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:80/", nil)
		req.Host = host
		resp, err := noFollow.Do(req)
		if err != nil {
			t.Fatalf("probe %s: %v", host, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return resp.StatusCode, resp.Header.Get("Location"), string(b)
	}
	if code, loc, _ := probeHost("panel.test.invalid"); code != 308 || !strings.HasPrefix(loc, "https://panel.test.invalid/") {
		t.Errorf("panel site: want 308 -> https://panel.test.invalid/, got %d %q", code, loc)
	}
	if code, _, body := probeHost("app.test.invalid"); code != 200 || body != "origin" {
		t.Errorf("resource site: want proxied 200 origin, got %d %q", code, body)
	}
	if code, loc, _ := probeHost("www.app.test.invalid"); code != 308 || !strings.HasPrefix(loc, "https://app.test.invalid/") {
		t.Errorf("www alias: want 308 -> https://app.test.invalid/, got %d %q", code, loc)
	}

	// 4. Idempotence: re-rendering the same sites and loading again must report no drift, which is
	// the property the reconciler loop depends on to stay quiet at steady state.
	again, hash2, err := edge.Render(sites, opts)
	if err != nil {
		t.Fatal(err)
	}
	if hash2 != hash {
		t.Fatalf("render is not deterministic: %s != %s", hash2, hash)
	}
	running, err := c.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edge.Canonical(running) != edge.Canonical(again) {
		t.Fatal("drift after loading the identical config")
	}

	// 5. A domain change reaches Caddy through /load only (zero-downtime swap, ADR 0003).
	sites = append(sites, edge.Site{Hostname: "two.test.invalid", Upstream: origin})
	cfg2, hash3, err := edge.Render(sites, opts)
	if err != nil {
		t.Fatal(err)
	}
	if edge.Canonical(cfg2) == edge.Canonical(running) {
		t.Fatal("render did not change after adding a site")
	}
	if err := c.Load(ctx, cfg2); err != nil {
		t.Fatalf("load second config: %v", err)
	}
	if code, _, body := probeHost("two.test.invalid"); code != 200 || body != "origin" {
		t.Errorf("added site: want proxied 200 origin, got %d %q", code, body)
	}
	running, _ = c.Running(ctx)
	if edge.Canonical(running) != edge.Canonical(cfg2) {
		t.Fatal("drift after loading the new config")
	}
	t.Logf("loaded two configs, hash %s -> %s, admin socket %s", hash[:8], hash3[:8], admin)
}
