package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/modules/terminal"
)

// ---- fakes ----

type fakeProc struct {
	mu     sync.Mutex
	in     strings.Builder
	out    chan []byte
	closed bool
}

func newFakeProc() *fakeProc { return &fakeProc{out: make(chan []byte, 8)} }
func (f *fakeProc) Read(b []byte) (int, error) {
	d, ok := <-f.out
	if !ok {
		return 0, io.EOF
	}
	return copy(b, d), nil
}
func (f *fakeProc) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.in.Write(b)
}
func (f *fakeProc) Resize(int, int) error { return nil }
func (f *fakeProc) ExitCode() int         { return 0 }
func (f *fakeProc) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		f.closed = true
		close(f.out)
	}
	return nil
}
func (f *fakeProc) input() string   { f.mu.Lock(); defer f.mu.Unlock(); return f.in.String() }
func (f *fakeProc) wasClosed() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.closed }

type fakeSpawner struct{ p *fakeProc }

func (s fakeSpawner) StartHost(context.Context, terminal.HelperRequest) (terminal.Proc, error) {
	return s.p, nil
}

type fakeRuntime struct {
	has   bool
	lines []runtime.LogLine
	ex    runtime.ExecStream
}

func (f *fakeRuntime) Available(context.Context) error { return nil }
func (f *fakeRuntime) Resolve(_ context.Context, id string) (runtime.Container, error) {
	if !f.has {
		return runtime.Container{}, runtime.ErrNoContainer
	}
	return runtime.Container{ID: "abcdef123456", Name: "shop", Running: true, ResourceID: id}, nil
}
func (f *fakeRuntime) Logs(ctx context.Context, _ string, o runtime.LogOptions, emit runtime.LogFunc) error {
	for _, l := range f.lines {
		if err := emit(l); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeRuntime) Exec(context.Context, string, []string, bool, int, int) (runtime.ExecStream, error) {
	if f.ex == nil {
		return nil, runtime.ErrUnavailable
	}
	return f.ex, nil
}

// fakeExec is a container exec stream: output is pushed on out, stdin is captured, and Wait reports code.
type fakeExec struct {
	out        chan []byte
	mu         sync.Mutex
	stdin      strings.Builder
	cols, rows int
	closed     bool
	code       int
}

func newFakeExec(code int) *fakeExec {
	return &fakeExec{out: make(chan []byte, 8), code: code}
}
func (e *fakeExec) Stdin() io.WriteCloser { return execStdin{e} }
func (e *fakeExec) Stdout() io.Reader     { return execOut{e} }
func (e *fakeExec) Resize(c, r int) error {
	e.mu.Lock()
	e.cols, e.rows = c, r
	e.mu.Unlock()
	return nil
}
func (e *fakeExec) Wait(context.Context) (int, error) { return e.code, nil }
func (e *fakeExec) Close() error {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		close(e.out)
	}
	e.mu.Unlock()
	return nil
}
func (e *fakeExec) size() (int, int) { e.mu.Lock(); defer e.mu.Unlock(); return e.cols, e.rows }
func (e *fakeExec) input() string    { e.mu.Lock(); defer e.mu.Unlock(); return e.stdin.String() }
func (e *fakeExec) wasClosed() bool  { e.mu.Lock(); defer e.mu.Unlock(); return e.closed }

type execStdin struct{ e *fakeExec }

func (s execStdin) Write(b []byte) (int, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	return s.e.stdin.Write(b)
}
func (s execStdin) Close() error { return nil }

type execOut struct{ e *fakeExec }

func (o execOut) Read(b []byte) (int, error) {
	d, ok := <-o.e.out
	if !ok {
		return 0, io.EOF
	}
	return copy(b, d), nil
}

// ---- helpers ----

func (e *env) enableTerminal(proc *fakeProc, rt *fakeRuntime) {
	e.t.Helper()
	e.s.Terminals = terminal.NewStore(e.s.DB)
	e.s.Tickets = terminal.NewTickets(terminal.TicketTTL)
	e.s.HostShells = fakeSpawner{proc}
	e.s.Runtime = rt
	if _, err := e.s.DB.Exec(`UPDATE setup_state SET operator='ops' WHERE id=1`); err != nil {
		e.t.Fatal(err)
	}
}

func ticket(t *testing.T, c *client, body map[string]any) string {
	t.Helper()
	w, b := c.post("/terminal/ticket", body)
	if w.Code != 201 {
		t.Fatalf("ticket: %d %v", w.Code, b)
	}
	return b["ticket"].(string)
}

func wsURL(srv *httptest.Server, path string) string {
	return "ws" + strings.TrimPrefix(srv.URL, "http") + Prefix + path
}

func (c *client) header() http.Header {
	h := http.Header{}
	h.Set("Cookie", c.cookie.Name+"="+c.cookie.Value)
	return h
}

// ---- tests ----

// expectRefused dials and requires the server to refuse: either the handshake fails (no session) or it completes
// and the server sends an {type:"error"} frame followed by close 1008, which is what xterm shows the user.
func expectRefused(t *testing.T, url string, h http.Header) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(data), `"type":"error"`) {
		t.Fatalf("want an error frame first, got typ=%v data=%q err=%v", typ, data, err)
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("want close 1008, got %v", err)
	}
}

func TestTerminalTicketRules(t *testing.T) {
	e := newEnv(t)
	c, secret := e.bootstrap()
	if w, b := c.post("/terminal/ticket", map[string]any{"target": "host"}); w.Code != 409 || b["details"].(map[string]any)["reason"] != "operator_missing" {
		t.Fatalf("no operator: %d %v", w.Code, b)
	}
	e.enableTerminal(newFakeProc(), &fakeRuntime{})
	if w, _ := c.post("/terminal/ticket", map[string]any{"target": "bogus"}); w.Code != 422 {
		t.Fatalf("bad target must be 422, got %d", w.Code)
	}
	if w, _ := c.post("/terminal/ticket", map[string]any{"target": "container"}); w.Code != 422 {
		t.Fatalf("container without resource_id must be 422, got %d", w.Code)
	}
	if w, _ := c.post("/terminal/ticket", map[string]any{"target": "container", "resource_id": "res_x"}); w.Code != 409 {
		t.Fatalf("a stopped container must be 409, got %d", w.Code)
	}
	if w, _ := c.post("/terminal/ticket", map[string]any{"target": "host", "as_root": true}); w.Code != 403 {
		t.Fatalf("root while disabled must be 403, got %d", w.Code)
	}
	e.s.DB.Exec(`UPDATE settings SET value='true' WHERE key='terminal.allow_root'`)
	if w, b := c.post("/terminal/ticket", map[string]any{"target": "host", "as_root": true}); w.Code != 403 || b["code"] != "step_up_required" {
		t.Fatalf("root needs step-up: %d %v", w.Code, b)
	}
	c.post("/auth/step-up", map[string]string{"code": codeAt(t, secret, -30*time.Second)})
	ticket(t, c, map[string]any{"target": "host", "as_root": true})
	ticket(t, c, map[string]any{"target": "host"})
}

func TestTerminalHostBridgeEndToEnd(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	proc := newFakeProc()
	e.enableTerminal(proc, &fakeRuntime{})
	srv := httptest.NewServer(e.h)
	defer srv.Close()

	tk := ticket(t, c, map[string]any{"target": "host"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL(srv, "/terminal/host?ticket="+tk), &websocket.DialOptions{HTTPHeader: c.header()})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ls\n")); err != nil {
		t.Fatal(err)
	}
	proc.out <- []byte("file.txt\r\n")
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "file.txt\r\n" {
		t.Fatalf("pty output must arrive as a binary frame: %v %v %q", typ, err, data)
	}
	for i := 0; i < 50 && proc.input() != "ls\n"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if proc.input() != "ls\n" {
		t.Fatalf("keystrokes must reach the shell, got %q", proc.input())
	}

	// A second upgrade with the same ticket is refused.
	expectRefused(t, wsURL(srv, "/terminal/host?ticket="+tk), c.header())

	// Closing the tab ends the shell and the session row.
	conn.Close(websocket.StatusNormalClosure, "bye")
	var ended, reason string
	for i := 0; i < 100; i++ {
		e.s.DB.QueryRow(`SELECT IFNULL(ended_at,''), end_reason FROM terminal_sessions`).Scan(&ended, &reason)
		if ended != "" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if ended == "" || !proc.wasClosed() {
		t.Fatalf("closing the socket must close the shell and the session (ended=%q closed=%v)", ended, proc.wasClosed())
	}
	var n int
	e.s.DB.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action IN ('terminal.ticket_issued','terminal.session_opened')`).Scan(&n)
	if n != 2 {
		t.Fatalf("want ticket_issued + session_opened audit rows, got %d", n)
	}
	if w, b := c.do("GET", "/terminal/sessions", nil, nil); w.Code != 200 || len(b["items"].([]any)) != 1 {
		t.Fatalf("sessions list: %d %v", w.Code, b)
	}
}

func TestTerminalRefusesWrongTicketAndUser(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	e.enableTerminal(newFakeProc(), &fakeRuntime{})
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	expectRefused(t, wsURL(srv, "/terminal/host?ticket=tk_nope"), c.header())
	if _, _, err := websocket.Dial(ctx, wsURL(srv, "/terminal/host"), nil); err == nil {
		t.Fatal("no session cookie must be refused")
	}
	// A host ticket cannot open a container terminal.
	tk := ticket(t, c, map[string]any{"target": "host"})
	expectRefused(t, wsURL(srv, "/terminal/resources/res_1?ticket="+tk), c.header())
}

func TestTerminalSessionLimit(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	e.enableTerminal(newFakeProc(), &fakeRuntime{})
	e.s.DB.Exec(`UPDATE settings SET value='1' WHERE key='terminal.max_sessions_per_user'`)
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, _, err := websocket.Dial(ctx, wsURL(srv, "/terminal/host?ticket="+ticket(t, c, map[string]any{"target": "host"})), &websocket.DialOptions{HTTPHeader: c.header()})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(websocket.StatusNormalClosure, "")
	expectRefused(t, wsURL(srv, "/terminal/host?ticket="+ticket(t, c, map[string]any{"target": "host"})), c.header())
}

func readSSE(t *testing.T, body io.Reader, want int) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(body)
	for sc.Scan() && len(out) < want*8 {
		out = append(out, sc.Text())
		if strings.HasPrefix(sc.Text(), "event: end") {
			break
		}
	}
	return out
}

func TestLogsStreamIDsAndResume(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	t0 := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	rt := &fakeRuntime{has: true, lines: []runtime.LogLine{
		{Time: t0, Line: "one"}, {Time: t0.Add(time.Second), Line: "two"}, {Time: t0.Add(2 * time.Second), Line: "three"}}}
	e.enableTerminal(newFakeProc(), rt)
	srv := httptest.NewServer(e.h)
	defer srv.Close()

	get := func(lastID string) string {
		req, _ := http.NewRequest("GET", srv.URL+Prefix+"/resources/res_1/logs?tail=10", nil)
		req.Header.Set("Cookie", c.cookie.Name+"="+c.cookie.Value)
		if lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("logs: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		return strings.Join(readSSE(t, resp.Body, 3), "\n")
	}
	full := get("")
	if !strings.Contains(full, "data: 2026-10-21T10:00:00Z one") || !strings.Contains(full, "id: ") || !strings.Contains(full, "event: end") {
		t.Fatalf("full stream:\n%s", full)
	}
	resumed := get("1792576801000000000") // 10:00:01 UTC in unix nanoseconds
	if strings.Contains(resumed, " one") || strings.Contains(resumed, " two") || !strings.Contains(resumed, " three") {
		t.Fatalf("a resumed stream must start after the last seen line:\n%s", resumed)
	}
}

func TestLogsNoContainerIs404(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	e.enableTerminal(newFakeProc(), &fakeRuntime{})
	if w, b := c.do("GET", "/resources/res_1/logs", nil, nil); w.Code != 404 || b["code"] != "not_found" {
		t.Fatalf("want 404 not_found, got %d %v", w.Code, b)
	}
}

func TestDropQueueDropsOldestAndCounts(t *testing.T) {
	q := newDropQueue()
	for i := 0; i < logQueueCap+10; i++ {
		q.push(runtime.LogLine{Line: "x"})
	}
	items, dropped, _ := q.drain()
	if len(items) != logQueueCap || dropped != 10 {
		t.Fatalf("want %d kept and 10 dropped, got %d and %d", logQueueCap, len(items), dropped)
	}
}

// waitSessionClosed polls until the session row carries an end, then returns its reason.
func waitSessionClosed(t *testing.T, e *env) (string, string) {
	t.Helper()
	var ended, reason string
	for i := 0; i < 150; i++ {
		e.s.DB.QueryRow(`SELECT IFNULL(ended_at,''), end_reason FROM terminal_sessions`).Scan(&ended, &reason)
		if ended != "" {
			return ended, reason
		}
		time.Sleep(30 * time.Millisecond)
	}
	return ended, reason
}

// TestTerminalIdleTimeoutClosesTheShell covers the §6.2 P3 criterion "idle timeout ... enforced server-side":
// a shell with no input must be ended by the server, its process closed, and the reason recorded.
func TestTerminalIdleTimeoutClosesTheShell(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	proc := newFakeProc()
	e.enableTerminal(proc, &fakeRuntime{})
	if _, err := e.s.DB.Exec(`UPDATE settings SET value='1' WHERE key='terminal.idle_timeout_s'`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tk := ticket(t, c, map[string]any{"target": "host"})
	conn, _, err := websocket.Dial(ctx, wsURL(srv, "/terminal/host?ticket="+tk), &websocket.DialOptions{HTTPHeader: c.header()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// The contract, taken from the executable mock: the reason comes in-band as an error frame, then the
	// connection closes. An in-band frame is what the browser can depend on, so assert it first.
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("the idle timeout must tell the browser why: %v", err)
	}
	if typ != websocket.MessageText || !strings.Contains(string(data), `"type":"error"`) || !strings.Contains(string(data), "Idle timeout") {
		t.Fatalf("want an in-band idle error frame, got %v %q", typ, data)
	}
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("the idle timeout must close the connection")
	} else if sc := websocket.CloseStatus(err); sc != websocket.StatusNormalClosure {
		t.Fatalf("idle close must be reported as a normal closure, got %d (%v)", sc, err)
	}
	ended, reason := waitSessionClosed(t, e)
	if ended == "" || reason != "idle" {
		t.Fatalf("want an idle-closed session row, got ended=%q reason=%q", ended, reason)
	}
	if !proc.wasClosed() {
		t.Fatal("the idle timeout must close the shell process")
	}
}

// TestTerminalContainerBridge covers the container half of the shell contract: keystrokes reach the exec stdin,
// a text resize frame becomes a tty resize, and the exec exit code reaches the browser as an exit control frame.
func TestTerminalContainerBridge(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	ex := newFakeExec(7)
	e.enableTerminal(newFakeProc(), &fakeRuntime{has: true, ex: ex})
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tk := ticket(t, c, map[string]any{"target": "container", "resource_id": "res_1"})
	conn, _, err := websocket.Dial(ctx, wsURL(srv, "/terminal/resources/res_1?ticket="+tk), &websocket.DialOptions{HTTPHeader: c.header()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("id\r")); err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":132,"rows":43}`)); err != nil {
		t.Fatal(err)
	}
	ex.out <- []byte("uid=0(root)\r\n")
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "uid=0(root)\r\n" {
		t.Fatalf("container output must arrive as a binary frame: typ=%v err=%v data=%q", typ, err, data)
	}
	for i := 0; i < 50 && ex.input() != "id\r"; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if ex.input() != "id\r" {
		t.Fatalf("keystrokes must reach the exec stdin, got %q", ex.input())
	}
	for i := 0; i < 50 && func() bool { c, r := ex.size(); return c != 132 || r != 43 }(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if cols, rows := ex.size(); cols != 132 || rows != 43 {
		t.Fatalf("resize control frame must reach the tty, got %d x %d", cols, rows)
	}

	ex.Close() // the shell exits
	typ, data, err = conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(data), `"type":"exit"`) ||
		!strings.Contains(string(data), `"code":7`) {
		t.Fatalf("want an exit frame with code 7, got typ=%v data=%q err=%v", typ, data, err)
	}
	ended, _ := waitSessionClosed(t, e)
	var target, name, asUser string
	e.s.DB.QueryRow(`SELECT target, target_name, as_user FROM terminal_sessions`).Scan(&target, &name, &asUser)
	if ended == "" || target != "container" || name != "shop" || asUser != "container" {
		t.Fatalf("container session row wrong: ended=%q %+v %q %q", ended, target, name, asUser)
	}
}

// TestTerminalTicketIsBoundToOrigin: a ticket minted for one Origin must not open a shell from another. The
// upgrade is refused before any session row exists, so there is nothing to close.
func TestTerminalTicketIsBoundToOrigin(t *testing.T) {
	e := newEnv(t)
	c, _ := e.bootstrap()
	e.enableTerminal(newFakeProc(), &fakeRuntime{})
	srv := httptest.NewServer(e.h)
	defer srv.Close()
	w, b := c.do("POST", "/terminal/ticket", map[string]any{"target": "host"},
		map[string]string{"X-CSRF-Token": c.csrf, "Origin": "https://panel.example"})
	if w.Code != 201 {
		t.Fatalf("ticket: %d %v", w.Code, b)
	}
	h := c.header()
	h.Set("Origin", "https://evil.example")
	expectRefused(t, wsURL(srv, "/terminal/host?ticket="+b["ticket"].(string)), h)
	var n int
	e.s.DB.QueryRow(`SELECT COUNT(*) FROM terminal_sessions`).Scan(&n)
	if n != 0 {
		t.Fatalf("a mismatched Origin must not open a session, found %d rows", n)
	}
}
