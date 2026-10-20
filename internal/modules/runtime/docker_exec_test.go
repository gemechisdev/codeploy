package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubDaemon is a minimal Docker Engine API server on a unix socket, shaped like the daemon this adapter has to
// drive: it creates execs, answers resize, hijacks attach, and — as measured on Docker 29.1.3 — answers 404
// "page not found" for POST /exec/{id}/kill. It records what the client writes to an attached exec and whether the
// client half-closed, which is the only way to test session teardown without a container runtime.
type stubDaemon struct {
	ln net.Listener

	mu          sync.Mutex
	startBodies [][]byte // the body of every exec start, decoded before the hijack
	written     bytes.Buffer
	sawEOF      chan struct{}
}

func newStub(t *testing.T) (*stubDaemon, string) {
	t.Helper()
	// A unix socket needs a short path (Windows caps the name length), so it goes directly under the test's temp
	// dir rather than in a nested one.
	dir := t.TempDir()
	sock := filepath.Join(dir, "d.sock")
	if len(sock) > 100 {
		t.Skipf("socket path too long on this platform (%d bytes): %s", len(sock), sock)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix socket support on this platform (%v): teardown is then only proven by the live-daemon "+
			"spike in test/spikes — this is a skip, not a pass", err)
	}
	d := &stubDaemon{ln: ln, sawEOF: make(chan struct{})}
	go d.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return d, sock
}

func (s *stubDaemon) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *stubDaemon) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	path := req.URL.Path
	switch {
	case strings.HasSuffix(path, "/exec") && req.Method == "POST":
		reply(conn, "201 Created", `{"Id":"`+execID+`"}`)
	case strings.HasSuffix(path, "/start"):
		// Drain the start body first: http.ReadRequest leaves it in the buffer, and after the 101 every byte in
		// that buffer is tty traffic. Real daemons do the same separation before they start streaming.
		body, _ := io.ReadAll(req.Body)
		s.mu.Lock()
		s.startBodies = append(s.startBodies, body)
		s.mu.Unlock()
		if _, err := fmt.Fprint(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n"); err != nil {
			return
		}
		buf := make([]byte, 4096)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				s.mu.Lock()
				s.written.Write(buf[:n])
				s.mu.Unlock()
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					close(s.sawEOF)
				}
				return
			}
		}
	case strings.HasSuffix(path, "/kill"):
		// Exactly what Docker 29.1.3 answers: the route is not registered.
		reply(conn, "404 Not Found", `{"message":"page not found"}`)
	case strings.Contains(path, "/resize"):
		reply(conn, "200 OK", "")
	default:
		reply(conn, "404 Not Found", `{"message":"page not found"}`)
	}
}

func reply(conn net.Conn, status, body string) {
	fmt.Fprintf(conn, "HTTP/1.1 %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		status, len(body), body)
}

const execID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestDockerCloseEndsTheShellItself proves the session-teardown contract on a daemon that cannot kill an exec:
// the adapter must make the shell read EOF itself, because closing the attach leaves the process running (measured
// against Docker 29.1.3, see docs/DECISIONS.md). A closed tab that leaks a live shell is a security defect, not a
// resource leak.
func TestDockerCloseEndsTheShellItself(t *testing.T) {
	d, sock := newStub(t)
	rt := NewDocker(sock)
	ctx := context.Background()

	ex, err := rt.Exec(ctx, strings.Repeat("ab", 20), []string{"/bin/sh"}, true, 80, 24)
	if err != nil {
		t.Fatalf("exec create+attach: %v", err)
	}

	if err := ex.Close(); err != nil {
		t.Fatalf("Close must tolerate a daemon with no kill route, got: %v", err)
	}

	select {
	case <-d.sawEOF:
	case <-time.After(3 * time.Second):
		t.Fatal("Close must half-close the attach so the daemon sees the session end")
	}

	d.mu.Lock()
	got := d.written.String()
	startBodies := append([][]byte(nil), d.startBodies...)
	d.mu.Unlock()

	// ^U kills any half-typed line (teardown must not execute a partial command), then ^D twice is the EOF the
	// shell exits on. Anything else in the stream would be the panel typing into the user's session.
	if got != ttyEOF {
		t.Fatalf("Close must send exactly the terminal EOF gesture %q, got %q", ttyEOF, got)
	}

	// The start body is the other half of the contract: a tty exec needs stdin attached or input never arrives.
	if len(startBodies) == 0 {
		t.Fatal("the adapter never started the exec attach")
	}
	var start struct {
		Detach      bool     `json:"Detach"`
		Tty         bool     `json:"Tty"`
		AttachStdin bool     `json:"AttachStdin"`
		Cmd         []string `json:"Cmd"`
	}
	if err := json.Unmarshal(startBodies[0], &start); err != nil {
		t.Fatalf("exec start body %q: %v", startBodies[0], err)
	}
	if start.Detach || !start.Tty {
		t.Fatalf("exec start must attach (Detach false) a tty, got %s", startBodies[0])
	}

	// Close is idempotent: a second call must not write anything else into the (already gone) session.
	before := len(got)
	if err := ex.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	d.mu.Lock()
	after := d.written.Len()
	d.mu.Unlock()
	if after != before {
		t.Fatalf("Close must be idempotent, wrote %d extra bytes", after-before)
	}
}

// TestDockerExecRefusesNonTTY keeps the port honest: the terminal's contract is a pty, and a daemon that is asked
// for a non-tty exec would echo input back with no line discipline.
func TestDockerExecRefusesNonTTY(t *testing.T) {
	_, sock := newStub(t)
	if _, err := NewDocker(sock).Exec(context.Background(), strings.Repeat("ab", 20), []string{"/bin/sh"}, false, 80, 24); err == nil {
		t.Fatal("non-tty exec must be refused, not silently half-supported")
	}
}
