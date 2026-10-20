//go:build spike

// Spike E: the host shell through the real privilege boundary — systemd socket activation, SO_PEERCRED, the
// operator-account predicate and the process-group kill. The unit tests for this path substitute a fake
// connection, so five claims can only be settled on a real host with a real systemd:
//
//  1. the socket's mode and its directory's mode let the unprivileged codeplo service user connect;
//  2. the helper rejects a peer whose uid is not the service user, even when that peer is root;
//  3. Authorize's operator predicate works against the real /etc/passwd and /etc/group — a sudo member gets a
//     shell, a login account without sudo does not, system accounts do not, root only with the flag file;
//  4. the shell really runs as the target user, with that user's HOME and a tty of the resized size;
//  5. closing the connection kills the whole process group — the "close the tab ends the PTY" requirement.
//
// Setup as root (tmp/setup_spike_e.sh installs the units and creates the two accounts), then run the compiled
// binary twice — once as the service user, once as root for the peer check:
//
//	CGO_ENABLED=0 GOOS=linux go test -c -tags spike -o bin/spikee_linux_amd64 ./test/spikes
//	setpriv --reuid=$(id -u codeplo) --regid=$(id -g codeplo) --clear-groups \
//	  env CPL_OP_USER=cpl-op CPL_OP_UID=$(id -u cpl-op) CPL_BAD_USER=cpl-nop \
//	  /root/spikee -test.run 'TestSpikeH(Operator|Refuses|Root|Close)' -test.v
//	# as root, with the same env:  -test.run TestSpikeHPeer
//
// It skips when the helper socket is absent and says so out loud — a skip is never a pass.
package spikes

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/modules/terminal"
)

func spikeSocket() string {
	if s := os.Getenv("CPL_PTY_SOCK"); s != "" {
		return s
	}
	return "/run/codeplo-pty/pty.sock"
}

// envOrFail fails rather than skips: a spike whose inputs are missing has verified nothing, and silently skipping
// the host-shell proof is how P3 would end up reported as done while never having run.
func envOrFail(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s is not set, so the host-shell spike cannot run — this is not a pass", key)
	}
	return v
}

func requireSocket(t *testing.T) terminal.HelperSpawner {
	t.Helper()
	if _, err := os.Stat(spikeSocket()); err != nil {
		t.Skipf("no helper socket at %s: spike NOT run (skip, not a pass): %v", spikeSocket(), err)
	}
	return terminal.HelperSpawner{Socket: spikeSocket()}
}

// ptyRead pulls from the shell stream until want appears or the deadline passes, and fails the test when it does
// not. Reads happen in a goroutine so a silent shell times out instead of hanging the run.
func ptyRead(t *testing.T, r io.Reader, want string, timeout time.Duration) string {
	t.Helper()
	ch := make(chan string, 32)
	go func() {
		buf := make([]byte, 8192)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				ch <- string(buf[:n])
			}
			if err != nil {
				close(ch)
				return
			}
		}
	}()
	var got strings.Builder
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case s, ok := <-ch:
			if !ok {
				t.Fatalf("the shell stream ended before %q appeared: %q", want, got.String())
			}
			got.WriteString(s)
			if strings.Contains(got.String(), want) {
				return got.String()
			}
		case <-time.After(100 * time.Millisecond):
		}
	}
	t.Fatalf("never saw %q in the shell output: %q", want, got.String())
	return ""
}

func TestSpikeHOperatorShellEndToEnd(t *testing.T) {
	sp := requireSocket(t)
	opUser := envOrFail(t, "CPL_OP_USER")
	opUID := envOrFail(t, "CPL_OP_UID")

	// Claims 1 and 4: the unprivileged service user can reach the socket at all, and the shell it gets belongs to
	// the operator rather than to whoever owns the panel process.
	p, err := sp.StartHost(context.Background(), terminal.HelperRequest{User: opUser, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatalf("the operator account %q must get a host shell — peer uid, SocketMode, DirectoryMode and Authorize "+
			"all have to line up: %v", opUser, err)
	}
	defer p.Close()
	t.Logf("prompt: %q", ptyRead(t, p, "#", 10*time.Second))

	if _, err := p.Write([]byte("id -u; echo HOME=$HOME; echo MARK-OK\r")); err != nil {
		t.Fatalf("write keystrokes: %v", err)
	}
	out := ptyRead(t, p, "MARK-OK", 10*time.Second)
	if !strings.Contains(out, opUID) {
		t.Fatalf("the shell is not running as uid %s, it reported: %q", opUID, out)
	}
	if strings.Contains(out, "HOME=\r\n") || strings.Contains(out, "HOME=/root") {
		t.Fatalf("the shell did not get the operator's HOME: %q", out)
	}

	// Claim 4, the tty: resize, then ask the kernel how big the terminal actually is.
	if err := p.Resize(120, 40); err != nil {
		t.Fatalf("resize frame: %v", err)
	}
	if _, err := p.Write([]byte("stty size\r")); err != nil {
		t.Fatalf("write stty: %v", err)
	}
	t.Logf("stty confirms the resized tty: %q", strings.TrimSpace(ptyRead(t, p, "40 120", 10*time.Second)))

	// A typed logout has to carry its exit code back, because that is what the browser is shown.
	if _, err := p.Write([]byte("exit\r")); err != nil {
		t.Fatalf("write exit: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		buf := make([]byte, 512)
		_, err := p.Read(buf)
		if err != nil {
			if code := p.ExitCode(); code != 0 {
				t.Fatalf("a shell the user left with 'exit' must report code 0, got %d", code)
			}
			t.Logf("clean logout, exit code 0")
			return
		}
	}
	t.Fatal("the shell never reported an exit after 'exit'")
}

func TestSpikeHRefusesAccountsThatAreNotOperators(t *testing.T) {
	sp := requireSocket(t)
	bad := envOrFail(t, "CPL_BAD_USER")

	if _, err := sp.StartHost(context.Background(), terminal.HelperRequest{User: bad}); err == nil {
		t.Fatalf("a login account with no sudo membership must not get a host shell (%s was accepted)", bad)
	} else if !errors.Is(err, terminal.ErrBadUser) {
		t.Fatalf("want ErrBadUser for %s, got %v", bad, err)
	} else {
		t.Logf("refused the non-operator %s: %v", bad, err)
	}

	// The daemon must not be able to name its own service account, root, or anything shaped like an injection.
	for _, u := range []string{"root", "codeplo", "nobody", "cpl-op; rm -rf /", "-x", strings.Repeat("a", 64)} {
		if _, err := sp.StartHost(context.Background(), terminal.HelperRequest{User: u}); err == nil {
			t.Errorf("%q must not be reachable through the host shell", u)
		} else {
			t.Logf("refused %q: %v", u, err)
		}
	}
}

func TestSpikeHRootFollowsTheFlagFile(t *testing.T) {
	sp := requireSocket(t)
	wantAllowed := os.Getenv("CPL_ROOT_FLAG") == "1"
	p, err := sp.StartHost(context.Background(), terminal.HelperRequest{Root: true, Cols: 80, Rows: 24})
	if !wantAllowed {
		if err == nil {
			_ = p.Close()
			t.Fatal("a root shell must be refused until the host owner creates /etc/codeplo/allow-root")
		}
		if !errors.Is(err, terminal.ErrRootDisabled) {
			t.Fatalf("want ErrRootDisabled, got %v", err)
		}
		t.Logf("root shell refused as designed: %v", err)
		return
	}
	if err != nil {
		t.Fatalf("with the flag file present the host owner asked for root shells and got none: %v", err)
	}
	defer p.Close()
	ptyRead(t, p, "#", 10*time.Second)
	if _, err := p.Write([]byte("id -u; echo ROOT-MARK\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	out := ptyRead(t, p, "ROOT-MARK", 10*time.Second)
	if !strings.Contains(out, "\n0\r") && !strings.Contains(out, "0\r\n") {
		t.Fatalf("the root shell is not uid 0: %q", out)
	}
	t.Log("root shell granted only because the flag file exists")
}

// TestSpikeHPeerMustBeServiceUser is run as root: the helper must hang up on a peer that is not the service user,
// because reachability is not the boundary — the uid is.
func TestSpikeHPeerMustBeServiceUser(t *testing.T) {
	sp := requireSocket(t)
	if os.Geteuid() == 0 {
		_, err := sp.StartHost(context.Background(), terminal.HelperRequest{User: envOrFail(t, "CPL_OP_USER")})
		if err == nil {
			t.Fatal("the helper accepted a root peer: SO_PEERCRED is not enforced")
		}
		t.Logf("refused a root peer as designed: %v", err)
		return
	}
	t.Skip("this test only proves anything when run as root (skip, not a pass)")
}

// TestSpikeHCloseEndsTheProcessGroup is the §6.2 requirement "close the tab and the PTY ends", measured against
// the process table instead of trusted from the code.
func TestSpikeHCloseEndsTheProcessGroup(t *testing.T) {
	sp := requireSocket(t)
	p, err := sp.StartHost(context.Background(), terminal.HelperRequest{User: envOrFail(t, "CPL_OP_USER"), Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("start host shell: %v", err)
	}
	ptyRead(t, p, "#", 10*time.Second)

	// A marker no other process could be running, started in the foreground of the shell's own process group.
	marker := "spikee" + strconv.Itoa(os.Getpid())
	if _, err := p.Write([]byte("sh -c 'echo " + marker + "; sleep 300'\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	ptyRead(t, p, marker, 10*time.Second)
	if !psHas(marker) {
		t.Fatal("the marker process never appeared in the process table, so this test proved nothing")
	}

	// Closing the daemon's side of the socket is what the browser tab closing turns into.
	_ = p.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if !psHas(marker) {
			t.Logf("the session was gone with %.1f s of the 8 s window still left", time.Until(deadline).Seconds())
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	out, _ := exec.Command("ps", "-eo", "pid,uid,args").Output()
	var left []string
	for _, ln := range strings.Split(string(out), "\n") {
		if strings.Contains(ln, marker) || strings.Contains(ln, "sleep 300") {
			left = append(left, ln)
		}
	}
	t.Fatalf("closing the connection left the session running: the process-group kill does not work on a real host. %v", left)
}

func psHas(s string) bool {
	out, err := exec.Command("ps", "-eo", "args").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), s)
}
