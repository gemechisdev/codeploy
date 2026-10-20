//go:build spike

// Spike B: Docker Engine API over the unix socket, with no shell and no SDK: pull, create, start, wait for health, swap.
// Run: go test -tags spike ./test/spikes -run Docker -v   (needs /var/run/docker.sock; skips otherwise)
package spikes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

type dock struct{ c *http.Client }

func newDock() *dock {
	return &dock{&http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
	}}}}
}

func (d *dock) do(method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://docker"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.c.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, nil
}

func (d *dock) create(t *testing.T, name string) string {
	body := map[string]any{
		"Image": "nginx:alpine",
		"Healthcheck": map[string]any{"Test": []string{"CMD-SHELL", "wget -qO- http://127.0.0.1/ >/dev/null || exit 1"},
			"Interval": int64(time.Second), "Timeout": int64(2 * time.Second), "Retries": 10, "StartPeriod": int64(time.Second)},
		"Labels": map[string]string{"codeplo.spike": "1"},
		// CapDrop ALL is the isolation posture under test; the re-added capabilities are the
		// minimum set the probe image (nginx:alpine) needs to run at all: CHOWN for its
		// entrypoint's cache chown, NET_BIND_SERVICE to bind :80 as root, and SETGID/SETUID
		// for the master to drop workers to the nginx user (without them every worker dies
		// with "setgid(101) failed (EPERM)", the master exits, and no container can become
		// healthy - a wrong fixture, not a product defect; a no-CapDrop control reaches
		// healthy in ~6 s on this daemon, proving the healthcheck plumbing works.
		// See TEST_REPORT.md, Spike B.
		"HostConfig": map[string]any{"Memory": 128 << 20, "PidsLimit": 128, "CapDrop": []string{"ALL"}, "CapAdd": []string{"CHOWN", "NET_BIND_SERVICE", "SETGID", "SETUID"}},
	}
	st, b, err := d.do("POST", "/containers/create?name="+name, body)
	if err != nil || st != 201 {
		t.Fatalf("create %s: %d %s %v", name, st, b, err)
	}
	var out struct{ Id string }
	_ = json.Unmarshal(b, &out)
	if st, b, _ := d.do("POST", "/containers/"+out.Id+"/start", nil); st != 204 {
		t.Fatalf("start %s: %d %s", name, st, b)
	}
	return out.Id
}

func (d *dock) waitHealthy(t *testing.T, id string) {
	for i := 0; i < 60; i++ {
		_, b, _ := d.do("GET", "/containers/"+id+"/json", nil)
		var s struct {
			State struct{ Health struct{ Status string } }
		}
		_ = json.Unmarshal(b, &s)
		if s.State.Health.Status == "healthy" {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("container %s never became healthy", id)
}

func TestDockerPullCreateHealthSwap(t *testing.T) {
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not available: spike NOT run (this is a skip, not a pass)")
	}
	d := newDock()
	if st, b, err := d.do("POST", "/images/create?fromImage=nginx&tag=alpine", nil); err != nil || st != 200 {
		t.Fatalf("pull: %d %s %v", st, b, err)
	}
	suffix := fmt.Sprint(time.Now().Unix())
	old := d.create(t, "codeplo-spike-old-"+suffix)
	d.waitHealthy(t, old)
	next := d.create(t, "codeplo-spike-new-"+suffix) // blue/green: the new container must be healthy before the old one stops
	d.waitHealthy(t, next)
	if st, b, _ := d.do("POST", "/containers/"+old+"/stop?t=5", nil); st != 204 {
		t.Fatalf("stop old: %d %s", st, b)
	}
	for _, id := range []string{old, next} {
		_, _, _ = d.do("DELETE", "/containers/"+id+"?force=true", nil)
	}
}
