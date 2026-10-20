package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/modules/terminal"
)

// P3 terminal: single-use tickets, the host shell (via the privileged helper) and the container shell (via the
// runtime port). WebSocket protocol, matching the executable mock: binary frames are PTY bytes in both
// directions; text frames are JSON control, in {type:"resize",cols,rows}, out {type:"exit",code} or
// {type:"error",message}.

func (s *Server) terminalRoutes() []RouteDef {
	return []RouteDef{
		{Method: "POST", Path: "/terminal/ticket", Perm: "terminal:container", Module: "terminal", Handler: s.termTicket},
		{Method: "GET", Path: "/terminal/sessions", Perm: "terminal:host", Module: "terminal", Handler: s.termSessions},
		{Method: "GET", Path: "/terminal/host", Perm: "terminal:host", Module: "terminal", Handler: s.termHost},
		{Method: "GET", Path: "/terminal/resources/{id}", Perm: "terminal:container", Module: "terminal", Handler: s.termContainer},
	}
}

func (s *Server) operatorName(ctx context.Context) string {
	var op sql.NullString
	_ = s.DB.QueryRowContext(ctx, `SELECT operator FROM setup_state WHERE id=1`).Scan(&op)
	return op.String
}

func (s *Server) termTicket(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "terminal") {
		return
	}
	p := from(r)
	var b struct {
		Target     string  `json:"target"`
		ResourceID *string `json:"resource_id"`
		AsRoot     bool    `json:"as_root"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Target != "host" && b.Target != "container" {
		invalid(w, map[string]string{"target": "target must be host or container"})
		return
	}
	cfg, err := terminal.LoadConfig(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	resourceID := ""
	if b.Target == "host" {
		if !p.Perms["terminal:host"] {
			writeErr(w, 403, "forbidden", "Missing permission: terminal:host", nil)
			return
		}
		if s.operatorName(r.Context()) == "" {
			writeErr(w, 409, "conflict", "The host shell needs an operator identity. Configure one under Server → Security first.", map[string]any{"reason": "operator_missing"})
			return
		}
		if s.HostShells == nil {
			writeErr(w, 500, "internal", "The host shell is not available", nil)
			return
		}
	} else {
		if b.ResourceID == nil || *b.ResourceID == "" {
			invalid(w, map[string]string{"resource_id": "resource_id is required for a container shell"})
			return
		}
		if s.Runtime == nil {
			writeErr(w, 500, "internal", "The container runtime is not available", nil)
			return
		}
		if _, err := s.Runtime.Resolve(r.Context(), *b.ResourceID); err != nil {
			s.runtimeErr(w, err)
			return
		}
		resourceID = *b.ResourceID
	}
	if b.AsRoot {
		switch {
		case b.Target != "host":
			invalid(w, map[string]string{"as_root": "Root applies to the host shell only"})
			return
		case !cfg.AllowRoot:
			writeErr(w, 403, "forbidden", "The root shell is disabled in server settings", nil)
			return
		case !p.Perms["terminal:root"]:
			writeErr(w, 403, "forbidden", "Missing permission: terminal:root", nil)
			return
		case !requireStepUp(w, p):
			return
		}
	}
	if s.Tickets == nil {
		writeErr(w, 500, "internal", "Terminal tickets are not available", nil)
		return
	}
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "terminal.ticket_issued", "terminal", b.Target, map[string]any{"resource_id": nullable(resourceID), "as_root": b.AsRoot})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	tk, err := s.Tickets.Issue(p.User.ID, b.Target, resourceID, b.AsRoot, r.Header.Get("Origin"))
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"ticket": tk.ID, "expires_at": iso(tk.Expires), "idle_timeout_s": int(cfg.IdleTimeout / time.Second)})
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// runtimeErr maps runtime errors the way the port documents: no container is a conflict the user can fix by
// starting the resource; an unreachable runtime is a server problem and never a 404.
func (s *Server) runtimeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, runtime.ErrNoContainer):
		writeErr(w, 409, "conflict", "The container is not running", nil)
	case errors.Is(err, runtime.ErrUnavailable):
		writeErr(w, 500, "internal", "The container runtime is not available", nil)
	default:
		internalErr(w, err)
	}
}

func (s *Server) termSessions(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "terminal") {
		return
	}
	if s.Terminals == nil {
		writeErr(w, 500, "internal", "Terminal sessions are not available", nil)
		return
	}
	limit, cursor := page(r)
	rows, next, err := s.Terminals.List(r.Context(), limit, cursor)
	if err != nil {
		internalErr(w, err)
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for i := range rows {
		items = append(items, rows[i].JSON())
	}
	var nc any
	if next != "" {
		nc = next
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": nc})
}

// consumeTicket validates the ticket for this upgrade. Every failure looks the same (close 1008, one message), so
// the handshake is not an oracle for which check failed.
func (s *Server) consumeTicket(w http.ResponseWriter, r *http.Request, p *Principal, target, resourceID string) (terminal.Ticket, bool) {
	if s.Tickets == nil || s.Terminals == nil {
		writeErr(w, 500, "internal", "Terminals are not available", nil)
		return terminal.Ticket{}, false
	}
	tk, err := s.Tickets.Consume(r.URL.Query().Get("ticket"), p.User.ID, target, resourceID, r.Header.Get("Origin"))
	if err != nil {
		s.wsFail(w, r, "invalid or expired ticket")
		return terminal.Ticket{}, false
	}
	return tk, true
}

// wsFail refuses a terminal after the ticket was spent: it completes the upgrade, sends one {type:"error"} frame
// and closes with 1008, which is what the frontend (and its mock) shows to the user. A plain HTTP error would
// reach the browser as an anonymous 1006 close with no reason.
func (s *Server) wsFail(w http.ResponseWriter, r *http.Request, msg string) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already answered the request (bad Origin, not an upgrade)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	b, _ := json.Marshal(map[string]any{"type": "error", "message": msg})
	_ = conn.Write(ctx, websocket.MessageText, b)
	_ = conn.Close(websocket.StatusPolicyViolation, msg)
}

func queryInt(r *http.Request, k string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(k))
	return n
}

func (s *Server) termHost(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "terminal") {
		return
	}
	p := from(r)
	tk, ok := s.consumeTicket(w, r, p, "host", "")
	if !ok {
		return
	}
	cfg, err := terminal.LoadConfig(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	asUser := s.operatorName(r.Context())
	if tk.AsRoot {
		// Root is re-checked at upgrade time: the ticket is 30 s old at most, but the setting, the role and the
		// step-up window can all have changed since it was issued.
		if !cfg.AllowRoot || !p.Perms["terminal:root"] {
			s.wsFail(w, r, "the root shell is not permitted")
			return
		}
		if !p.StepUpUntil.After(time.Now()) {
			s.wsFail(w, r, "step-up required: re-enter your 2FA code and reconnect")
			return
		}
		asUser = "root"
	}
	if asUser == "" {
		s.wsFail(w, r, "the host shell needs an operator identity")
		return
	}
	if s.HostShells == nil {
		s.wsFail(w, r, "the host shell is not available")
		return
	}
	host, _ := os.Hostname()
	sess, ok := s.openSession(w, r, p, cfg, "host", host, asUser, tk.AsRoot)
	if !ok {
		return
	}
	proc, err := s.HostShells.StartHost(r.Context(), terminal.HelperRequest{User: asUser, Root: tk.AsRoot, Cols: queryInt(r, "cols"), Rows: queryInt(r, "rows")})
	if err != nil {
		s.endSession(sess.ID, "error")
		switch {
		case errors.Is(err, terminal.ErrHelperUnavailable):
			s.wsFail(w, r, "the host shell helper is not installed or not running")
		case errors.Is(err, terminal.ErrRootDisabled):
			s.wsFail(w, r, "the root shell is not enabled on this host")
		case errors.Is(err, terminal.ErrBadUser):
			s.wsFail(w, r, "the operator account cannot open a shell")
		default:
			s.wsFail(w, r, "could not start the shell")
		}
		return
	}
	s.bridge(w, r, sess.ID, cfg.IdleTimeout, proc, proc.Resize, proc.ExitCode, proc.Close)
}

func (s *Server) termContainer(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "terminal") {
		return
	}
	p := from(r)
	resID := chiParam(r, "id")
	if _, ok := s.consumeTicket(w, r, p, "container", resID); !ok {
		return
	}
	if s.Runtime == nil {
		s.wsFail(w, r, "the container runtime is not available")
		return
	}
	cfg, err := terminal.LoadConfig(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	c, err := s.Runtime.Resolve(r.Context(), resID)
	if err != nil {
		if errors.Is(err, runtime.ErrNoContainer) {
			s.wsFail(w, r, "the container is not running")
		} else {
			s.wsFail(w, r, "the container runtime is not available")
		}
		return
	}
	sess, ok := s.openSession(w, r, p, cfg, "container", c.Name, "container", false)
	if !ok {
		return
	}
	// Fixed argv: nothing from the request reaches the command line.
	ex, err := s.Runtime.Exec(r.Context(), c.ID, []string{"/bin/sh", "-c", "command -v bash >/dev/null 2>&1 && exec bash -l || exec sh"}, true, queryInt(r, "cols"), queryInt(r, "rows"))
	if err != nil {
		s.endSession(sess.ID, "error")
		s.wsFail(w, r, "could not start a shell in the container")
		return
	}
	rw := struct {
		io.Reader
		io.Writer
	}{ex.Stdout(), ex.Stdin()}
	exit := func() int {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		code, err := ex.Wait(ctx)
		if err != nil {
			return -1
		}
		return code
	}
	s.bridge(w, r, sess.ID, cfg.IdleTimeout, rw, ex.Resize, exit, ex.Close)
}

// openSession writes the terminal_sessions row (enforcing the per-user limit) and its audit event. It fails
// closed: if the audit row cannot be written the session is closed and no shell starts.
func (s *Server) openSession(w http.ResponseWriter, r *http.Request, p *Principal, cfg terminal.Config, target, targetName, asUser string, root bool) (*terminal.Session, bool) {
	sess := &terminal.Session{ID: newID("trm_"), UserID: p.User.ID, UserEmail: p.User.Email, IP: clientIP(r), Target: target,
		TargetName: targetName, AsUser: asUser, IsRoot: root, Recorded: false} // recording is not implemented: never claim it
	if err := s.Terminals.Open(r.Context(), sess, cfg.MaxPerUser); err != nil {
		if errors.Is(err, terminal.ErrLimit) {
			s.wsFail(w, r, "too many open terminal sessions; close one first")
		} else {
			s.wsFail(w, r, "could not open a terminal session")
		}
		return nil, false
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "terminal.session_opened", "terminal", sess.ID, map[string]any{"target": target, "as_root": root})
	})
	if err != nil {
		s.endSession(sess.ID, "error")
		s.wsFail(w, r, "could not record the session; no shell was started")
		return nil, false
	}
	return sess, true
}

func (s *Server) endSession(id, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Terminals.Close(ctx, id, reason)
}

// bridge upgrades to a WebSocket and pumps bytes between the browser and the shell until either side ends.
// It always closes the shell and the session row, whichever way the connection ends.
func (s *Server) bridge(w http.ResponseWriter, r *http.Request, sessionID string, idle time.Duration, rw io.ReadWriter,
	resize func(cols, rows int) error, exitCode func() int, closeShell func() error) {
	conn, err := websocket.Accept(w, r, nil) // default: Origin host must match the request Host
	if err != nil {
		_ = closeShell()
		s.endSession(sessionID, "error")
		return
	}
	conn.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Ending a session is a two-step act, and the order matters because of how coder/websocket works: cancelling
	// the read context tears the TCP socket down immediately, so anything still owed to the browser has to go out
	// first. The pump goroutines therefore only signal the end (halt); the main flow closes the shell, hands the
	// connection back to the browser with a close frame, and cancels last.
	stop := make(chan struct{})
	var haltOnce sync.Once
	halt := func() { haltOnce.Do(func() { close(stop) }) }

	var mu sync.Mutex
	reason := "closed"
	alive := true // false once the browser has stopped reading; nothing is worth writing to it then
	// claim records the first end reason and reports whether this caller won it. Only the winner tells the
	// browser how the shell ended: after an idle timeout the server killed a live shell, so reporting "exit"
	// there would be wrong.
	claim := func(v string) bool {
		mu.Lock()
		defer mu.Unlock()
		if reason != "closed" {
			return false
		}
		reason = v
		return true
	}
	say := func(m map[string]any) {
		mu.Lock()
		ok := alive
		mu.Unlock()
		if ok {
			notify(conn, m)
		}
	}
	timer := time.AfterFunc(idle, func() {
		if claim("idle") {
			say(map[string]any{"type": "error", "message": "Idle timeout: the session was closed"})
		}
		halt()
	})
	defer timer.Stop()

	go func() { // shell -> browser
		buf := make([]byte, 32<<10)
		for {
			n, err := rw.Read(buf)
			if n > 0 {
				if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					halt()
					return
				}
			}
			if err != nil {
				if claim("exit") {
					say(map[string]any{"type": "exit", "code": exitCode()})
				}
				halt()
				return
			}
		}
	}()

	go func() { // browser -> shell; any error here (tab closed, bad frame) ends the session
		defer func() {
			mu.Lock()
			alive = false
			mu.Unlock()
			halt()
		}()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			timer.Reset(idle) // input counts as activity; output does not
			switch typ {
			case websocket.MessageBinary:
				if _, err := rw.Write(data); err != nil {
					return
				}
			case websocket.MessageText:
				var m struct {
					Type       string `json:"type"`
					Cols, Rows int
				}
				if json.Unmarshal(data, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 {
					_ = resize(m.Cols, m.Rows)
				}
			}
		}
	}()

	select {
	case <-stop:
	case <-ctx.Done(): // the request itself went away: the browser is gone, nothing left to tell it
	}
	mu.Lock()
	final, stillThere := reason, alive
	mu.Unlock()
	// Closing the shell ends its process group (helper) or hangs up its tty (container), so a closed tab cannot
	// leave a shell running.
	_ = closeShell()
	if stillThere {
		// Only a server-initiated end has a close frame to deliver. It goes out while the browser's reader is
		// still parked, and the client's close reply is what releases that read; the parked read holds the
		// library's read mutex, so Close itself cannot finish first. Bound the wait, then cancel: by then the
		// frame is already on the wire, and a browser that vanished cannot pin this handler.
		status, text := websocket.StatusNormalClosure, final
		if final == "idle" {
			text = "idle timeout"
		}
		closed := make(chan struct{})
		go func() {
			_ = conn.Close(status, text)
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second):
		}
	}
	cancel()
	s.endSession(sessionID, final)
}

// notify sends one JSON control frame on its own short deadline. A server-initiated end writes its reason
// before the connection is torn down, so it must never ride the bridge context that is being cancelled.
func notify(conn *websocket.Conn, m map[string]any) {
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = conn.Write(ctx, websocket.MessageText, b)
}

func chiParam(r *http.Request, k string) string { return chi.URLParam(r, k) }
