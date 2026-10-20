//go:build spike

// Spike D: the P3 runtime adapter against a real Docker daemon. It drives internal/modules/runtime's own
// *Docker type over /var/run/docker.sock, because three claims in docs/DECISIONS.md can only be settled by
// a live daemon:
//
//  1. the daemon accepts the exec create body the adapter sends (AttachStderr together with Tty);
//  2. a hijacked attach moves bytes both ways and accepts a resize;
//  3. closing the attach ends the shell inside the container. If it does not, every closed browser tab leaks a
//     live shell — and the naive mechanism (hang up the pty and hope) is what this adapter used to rely on.
//     Measured here: Docker 29.1.3 leaves the exec running on hang-up, does not register POST /exec/{id}/kill at
//     any API version, and only ends a shell when the attach's write side is half-closed so the shell reads EOF.
//
// Build on Windows: CGO_ENABLED=0 GOOS=linux go test -c -tags spike -o bin/spiked_linux_amd64 ./test/spikes
// Run in WSL as root:  ./bin/spiked_linux_amd64 -test.run SpikeD -test.v
//
// It skips when no daemon answers, and always removes the container it created.
package spikes

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/modules/runtime"
)

const spikeResource = "res_spiked"

func spikeDock() *dock { return newDock() }

func spikeHasDocker(t *testing.T) *runtime.Docker {
	t.Helper()
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		t.Skip("docker socket not available: spike NOT run (this is a skip, not a pass)")
	}
	rt := runtime.NewDocker("/var/run/docker.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Available(ctx); err != nil {
		t.Skipf("no runtime answered: %v (skip, not a pass)", err)
	}
	return rt
}

// spikeContainer creates and starts a non-tty container carrying the label the adapter resolves on, so both
// the label path and the multiplexed-log path get exercised. Returns its id.
func spikeContainer(t *testing.T) string {
	t.Helper()
	dd := spikeDock()
	body := map[string]any{
		"Image": "nginx:alpine",
		"Cmd":   []string{"/bin/sh", "-c", "echo spike-stdout; echo spike-stderr >&2; sleep 600"},
		"Labels": map[string]string{
			runtime.LabelResource: spikeResource,
			"codeplo.spike":       "d",
		},
		"HostConfig": map[string]any{"Memory": 64 << 20, "PidsLimit": 64},
	}
	st, b, err := dd.do("POST", "/containers/create?name=cpl-spike-d", body)
	if err != nil || st != 201 {
		t.Fatalf("create: %d %s %v", st, b, err)
	}
	var out struct{ Id string }
	_ = json.Unmarshal(b, &out)
	t.Cleanup(func() {
		if st, b, _ := dd.do("DELETE", "/containers/"+out.Id+"?force=1", nil); st != 204 {
			t.Errorf("spike container left behind: %d %s", st, b)
		}
	})
	if st, b, _ := dd.do("POST", "/containers/"+out.Id+"/start", nil); st != 204 {
		t.Fatalf("start: %d %s", st, b)
	}
	return out.Id
}

// readUpTo pulls from the attach stream until `want` appears or the deadline passes.
func readUpTo(ex runtime.ExecStream, want string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	type chunk struct {
		s   string
		err error
	}
	ch := make(chan chunk, 8)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ex.Stdout().Read(buf)
			if n > 0 {
				ch <- chunk{string(buf[:n]), nil}
			}
			if err != nil {
				ch <- chunk{"", err}
				return
			}
		}
	}()
	var got strings.Builder
	for time.Now().Before(deadline) {
		select {
		case c := <-ch:
			got.WriteString(c.s)
			if c.err != nil {
				return got.String()
			}
			if want != "" && strings.Contains(got.String(), want) {
				return got.String()
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	return got.String()
}

func TestSpikeDResolveAndLogs(t *testing.T) {
	rt := spikeHasDocker(t)
	id := spikeContainer(t)
	ctx := context.Background()

	c, err := rt.Resolve(ctx, spikeResource)
	if err != nil {
		t.Fatalf("resolve by %s=%s: %v", runtime.LabelResource, spikeResource, err)
	}
	if !strings.HasPrefix(id, c.ID) && !strings.HasPrefix(c.ID, id) {
		t.Fatalf("resolve returned %s, expected the container we started (%s)", c.ID, id)
	}
	if !c.Running || c.ResourceID != spikeResource {
		t.Fatalf("resolved container is not running/labelled: %+v", c)
	}
	if _, err := rt.Resolve(ctx, "res_absent"); !errors.Is(err, runtime.ErrNoContainer) {
		t.Fatalf("an unlabelled resource must be ErrNoContainer, got %v", err)
	}

	var lines []runtime.LogLine
	if err := rt.Logs(ctx, c.ID, runtime.LogOptions{Tail: 10}, func(l runtime.LogLine) error {
		lines = append(lines, l)
		return nil
	}); err != nil {
		t.Fatalf("logs: %v", err)
	}
	var out, errp string
	for _, l := range lines {
		switch {
		case l.Stream == "stdout" && strings.Contains(l.Line, "spike-stdout"):
			out = l.Line
		case l.Stream == "stderr" && strings.Contains(l.Line, "spike-stderr"):
			errp = l.Line
		}
	}
	if out == "" || errp == "" {
		t.Fatalf("stdout and stderr must arrive on their own streams, got %+v", lines)
	}

	// The Last-Event-ID resume path: a reader that starts from "now" must not replay what came before.
	seen := time.Now().UTC().Add(time.Second)
	var resumed []runtime.LogLine
	if err := rt.Logs(ctx, c.ID, runtime.LogOptions{Tail: 10, Since: seen}, func(l runtime.LogLine) error {
		resumed = append(resumed, l)
		return nil
	}); err != nil {
		t.Fatalf("logs since: %v", err)
	}
	if len(resumed) > 0 {
		t.Fatalf("since=now must replay nothing, got %+v", resumed)
	}
	t.Logf("resolved %s (%s); %d lines demuxed, both streams separated", c.ID, c.Name, len(lines))
}

func TestSpikeDExecAttach(t *testing.T) {
	rt := spikeHasDocker(t)
	id := spikeContainer(t)
	ctx := context.Background()
	c, err := rt.Resolve(ctx, spikeResource)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("probing container %s as %s", id, c.ID)

	// This call is itself assertion 1: the create body carries AttachStdin/Stdout/Stderr with Tty=true.
	ex, err := rt.Exec(ctx, c.ID, []string{"/bin/sh"}, true, 100, 30)
	if err != nil {
		t.Fatalf("exec create+attach (Tty with AttachStderr) rejected by the daemon: %v", err)
	}
	if err := ex.Resize(132, 43); err != nil {
		t.Fatalf("resize: %v", err)
	}
	go func() {
		_, _ = ex.Stdin().Write([]byte("stty size\r"))
		_, _ = ex.Stdin().Write([]byte("id -u\r"))
		_, _ = ex.Stdin().Write([]byte("echo SPIKE-MARKER\r"))
	}()
	out := readUpTo(ex, "SPIKE-MARKER", 20*time.Second)
	if !strings.Contains(out, "SPIKE-MARKER") {
		t.Fatalf("exec output never reached us, got %q", out)
	}
	t.Logf("attach round trip: %q", strings.ReplaceAll(out, "\r\n", " | "))

	// Assertion 3, the one that decides whether a closed tab leaks a process.
	if err := ex.Close(); err != nil {
		t.Fatalf("close attach: %v", err)
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	code, err := ex.Wait(waitCtx)
	if err != nil {
		t.Fatalf("the exec was STILL RUNNING 10 s after Close(): %v. Close must half-close the attach so the shell "+
			"reads EOF — measured against Docker 29.1.3, hanging up the socket alone leaves the exec running, and "+
			"POST /exec/{id}/kill is not registered on this daemon (see docs/DECISIONS.md).", err)
	}
	t.Logf("the exec ended (code %d) when the attach was closed", code)

	// Independent confirmation from the daemon's own process table: no shell left behind. The container's own
	// command is `sh -c ...; sleep 600`, and busybox sh execs the last command, so a "sh" entry here is ours.
	deadline := time.Now().Add(5 * time.Second)
	var procs []string
	var leaked bool
	for {
		_, b, err := spikeDock().do("GET", "/containers/"+id+"/top", nil)
		if err != nil {
			t.Fatalf("top: %v", err)
		}
		var top struct{ Processes [][]string }
		_ = json.Unmarshal(b, &top)
		leaked = false
		procs = nil
		for _, p := range top.Processes {
			if len(p) == 0 {
				continue
			}
			cmd := p[len(p)-1]
			procs = append(procs, cmd)
			if cmd == "sh" || strings.HasSuffix(cmd, "/sh") {
				leaked = true
			}
		}
		if !leaked || time.Now().After(deadline) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if leaked {
		t.Fatalf("a shell survived the close: process table = %v", procs)
	}
	t.Logf("container process table after the close: %v", procs)
}
