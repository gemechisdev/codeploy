package terminal

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// ErrHelperUnavailable means the privileged PTY helper socket is absent or refuses connections. The daemon is an
// unprivileged service with NoNewPrivileges, so it cannot start a shell as the operator itself, and a shell
// spawned from its process tree could not use sudo. The helper lives outside that tree (see
// deploy/codeplo-pty.socket) and the host shell is simply unavailable until it is installed.
var ErrHelperUnavailable = errors.New("host shell helper is not installed or not running")

// Proc is a running host shell as the API layer sees it.
type Proc interface {
	io.ReadWriter // Read: PTY output until the shell exits (then io.EOF); Write: keystrokes
	Resize(cols, rows int) error
	ExitCode() int // valid once Read has returned io.EOF
	Close() error  // closes the socket; the helper kills the shell's process group
}

// Spawner starts host shells. The production implementation is HelperSpawner; tests substitute a fake.
type Spawner interface {
	StartHost(ctx context.Context, req HelperRequest) (Proc, error)
}

// HelperSpawner talks to the root-side helper over a unix socket.
type HelperSpawner struct{ Socket string }

func (h HelperSpawner) StartHost(ctx context.Context, req HelperRequest) (Proc, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "unix", h.Socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHelperUnavailable, err)
	}
	req.Cols, req.Rows = ClampSize(req.Cols, req.Rows)
	b, _ := json.Marshal(req)
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		conn.Close()
		return nil, err
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	line, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrHelperUnavailable, err)
	}
	var rep HelperReply
	if err := json.Unmarshal(line, &rep); err != nil || !rep.OK {
		conn.Close()
		switch rep.Error {
		case ErrRootDisabled.Error():
			return nil, ErrRootDisabled
		case ErrBadUser.Error():
			return nil, ErrBadUser
		}
		return nil, fmt.Errorf("host shell refused: %s", rep.Error)
	}
	conn.SetDeadline(time.Time{})
	return &helperProc{conn: conn, r: r}, nil
}

type helperProc struct {
	conn    net.Conn
	r       *bufio.Reader
	wmu     sync.Mutex
	pending []byte
	code    int
}

func (p *helperProc) Read(b []byte) (int, error) {
	for len(p.pending) == 0 {
		typ, payload, err := ReadFrame(p.r)
		if err != nil {
			// A broken socket is not a clean logout: report an abnormal code so the UI does not show "exit 0".
			p.code = -1
			return 0, io.EOF
		}
		switch typ {
		case FrameData:
			p.pending = payload
		case FrameExit:
			p.code, _ = ParseExit(payload)
			return 0, io.EOF
		}
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

func (p *helperProc) Write(b []byte) (int, error) {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	for off := 0; off < len(b); {
		end := off + 32<<10
		if end > len(b) {
			end = len(b)
		}
		if err := WriteFrame(p.conn, FrameData, b[off:end]); err != nil {
			return off, err
		}
		off = end
	}
	return len(b), nil
}

func (p *helperProc) Resize(cols, rows int) error {
	cols, rows = ClampSize(cols, rows)
	p.wmu.Lock()
	defer p.wmu.Unlock()
	return WriteFrame(p.conn, FrameResize, ResizePayload(cols, rows))
}

func (p *helperProc) ExitCode() int { return p.code }
func (p *helperProc) Close() error  { return p.conn.Close() }
