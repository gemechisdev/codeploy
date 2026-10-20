//go:build spike

// Spike A: start Caddy with a unix admin socket, add and remove a route under load, expect zero failed requests.
// Run: go test -tags spike ./test/spikes -run Caddy -v   (needs the `caddy` binary on PATH; skips otherwise)
package spikes

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestCaddyAddRemoveRouteNoDrops(t *testing.T) {
	bin, err := exec.LookPath("caddy")
	if err != nil {
		t.Skip("caddy binary not available: spike NOT run (this is a skip, not a pass)")
	}
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin.sock")
	cfg := fmt.Sprintf(`{"admin":{"listen":"unix/%s","origins":["caddy"]},"apps":{"http":{"servers":{"srv0":{"listen":["127.0.0.1:18080"],"routes":[
	 {"@id":"base","match":[{"path":["/base"]}],"handle":[{"handler":"static_response","body":"base"}]}]}}}}}`, admin)
	cf := filepath.Join(dir, "caddy.json")
	if err := os.WriteFile(cf, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "--config", cf)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	ac := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", admin)
	}}}
	for i := 0; ; i++ {
		if resp, err := ac.Get("http://caddy/config/"); err == nil {
			resp.Body.Close()
			break
		}
		if i > 50 {
			t.Fatal("caddy admin socket never came up")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Wait until the data-plane listener serves before generating load, so startup is not counted as a drop.
	for i := 0; ; i++ {
		if resp, err := http.Get("http://127.0.0.1:18080/base"); err == nil {
			resp.Body.Close()
			break
		}
		if i > 50 {
			t.Fatal("caddy data listener never came up")
		}
		time.Sleep(100 * time.Millisecond)
	}

	var fails, total atomic.Int64
	var firstErr atomic.Value
	stop := make(chan struct{})
	go func() {
		c := &http.Client{Timeout: 2 * time.Second}
		for {
			select {
			case <-stop:
				return
			default:
			}
			total.Add(1)
			resp, err := c.Get("http://127.0.0.1:18080/base")
			if err != nil || resp.StatusCode != 200 {
				fails.Add(1)
				if err != nil {
					firstErr.CompareAndSwap(nil, err.Error())
				} else {
					firstErr.CompareAndSwap(nil, resp.Status)
				}
			}
			if resp != nil {
				resp.Body.Close()
			}
		}
	}()

	route := `{"@id":"r1","match":[{"path":["/r1"]}],"handle":[{"handler":"static_response","body":"r1"}]}`
	for i := 0; i < 20; i++ {
		resp, err := ac.Post("http://caddy/config/apps/http/servers/srv0/routes", "application/json", bytes.NewBufferString(route))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("add route: %v %v", err, resp)
		}
		resp.Body.Close()
		req, _ := http.NewRequest(http.MethodDelete, "http://caddy/id/r1", nil)
		resp, err = ac.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("remove route: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	close(stop)
	t.Logf("requests=%d failures=%d first_error=%v", total.Load(), fails.Load(), firstErr.Load())
	if fails.Load() != 0 {
		t.Fatalf("%d dropped or failed requests during reloads", fails.Load())
	}
}
