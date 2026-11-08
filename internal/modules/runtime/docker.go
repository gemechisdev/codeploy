package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sort"
	"strings"
	"sync"
	"time"
)

// Docker implements Runtime over the Docker Engine API on a unix socket. No SDK and no shell: plain HTTP, with a
// manual connection hijack for exec attach. Access to the socket is root-equivalent, so the daemon only ever
// passes IDs it resolved itself and validates everything else before it reaches a URL.
type Docker struct {
	Socket string
	hc     *http.Client
}

func NewDocker(socket string) *Docker {
	return &Docker{Socket: socket, hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}}
}

var (
	resourceRe          = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	containerRe         = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
	_           Runtime = (*Docker)(nil)
)

func (d *Docker) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("docker %s: %s %s", path, resp.Status, bytes.TrimSpace(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *Docker) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/_ping", nil)
	if err != nil {
		return err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%w: ping returned %s", ErrUnavailable, resp.Status)
	}
	return nil
}

// Resolve returns the newest running container labelled codeplo.resource=<id>. During a blue/green swap two
// containers can briefly carry the label; the newest (by Created) is the one the edge is switching to.
func (d *Docker) Resolve(ctx context.Context, resourceID string) (Container, error) {
	if !resourceRe.MatchString(resourceID) {
		return Container{}, ErrNoContainer
	}
	filters, _ := json.Marshal(map[string][]string{"label": {LabelResource + "=" + resourceID}, "status": {"running"}})
	var list []struct {
		ID      string
		Names   []string
		Image   string
		State   string
		Created int64
		Labels  map[string]string
	}
	if err := d.get(ctx, "/containers/json?filters="+url.QueryEscape(string(filters)), &list); err != nil {
		return Container{}, err
	}
	if len(list) == 0 {
		return Container{}, ErrNoContainer
	}
	// Docker does not guarantee list order; pick the highest Created unix timestamp.
	sort.Slice(list, func(i, j int) bool { return list[i].Created > list[j].Created })
	c := list[0]
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	return Container{ID: c.ID, Name: name, Image: c.Image, Running: c.State == "running", ResourceID: c.Labels[LabelResource]}, nil
}

func (d *Docker) Logs(ctx context.Context, id string, opt LogOptions, emit LogFunc) error {
	if !containerRe.MatchString(id) {
		return ErrNoContainer
	}
	var info struct {
		Config struct{ Tty bool }
	}
	if err := d.get(ctx, "/containers/"+id+"/json", &info); err != nil {
		return err
	}
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}, "timestamps": {"1"}}
	if opt.Follow {
		q.Set("follow", "1")
	}
	if opt.Tail > 0 {
		q.Set("tail", strconv.Itoa(opt.Tail))
	}
	if !opt.Since.IsZero() {
		q.Set("since", fmt.Sprintf("%d.%09d", opt.Since.Unix(), opt.Since.Nanosecond()))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/"+id+"/logs?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("docker logs: %s", resp.Status)
	}
	br := bufio.NewReaderSize(resp.Body, 64<<10)
	if info.Config.Tty {
		return scanLines(br, "stdout", emit)
	}
	return demux(br, emit)
}

// demux reads Docker's multiplexed stream: 8-byte header (stream, 0,0,0, size uint32 BE) then payload. Payloads
// can split lines, so each stream keeps its own carry-over buffer.
func demux(r io.Reader, emit LogFunc) error {
	carry := map[byte][]byte{}
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return flushCarry(carry, emit)
			}
			return err
		}
		n := binary.BigEndian.Uint32(hdr[4:])
		if n > 4<<20 {
			return fmt.Errorf("docker log frame of %d bytes", n)
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(r, payload); err != nil {
			return err
		}
		st := hdr[0]
		buf := append(carry[st], payload...)
		for {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				break
			}
			if err := emitLine(buf[:i], streamName(st), emit); err != nil {
				return err
			}
			buf = buf[i+1:]
		}
		carry[st] = append([]byte(nil), buf...)
	}
}

func flushCarry(carry map[byte][]byte, emit LogFunc) error {
	for st, b := range carry {
		if len(b) > 0 {
			if err := emitLine(b, streamName(st), emit); err != nil {
				return err
			}
		}
	}
	return nil
}

func scanLines(r *bufio.Reader, stream string, emit LogFunc) error {
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if e := emitLine(bytes.TrimRight(line, "\n"), stream, emit); e != nil {
				return e
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func streamName(b byte) string {
	if b == 2 {
		return "stderr"
	}
	return "stdout"
}

// emitLine splits "<RFC3339Nano> <text>" and hands the line to emit. A line without a parseable timestamp is
// still delivered, with the zero time, rather than dropped.
func emitLine(b []byte, stream string, emit LogFunc) error {
	s := strings.TrimRight(string(b), "\r")
	ll := LogLine{Stream: stream, Line: s}
	if i := strings.IndexByte(s, ' '); i > 0 {
		if t, err := time.Parse(time.RFC3339Nano, s[:i]); err == nil {
			ll.Time, ll.Line = t, s[i+1:]
		}
	}
	return emit(ll)
}

// ---- exec ----

func (d *Docker) Exec(ctx context.Context, id string, cmd []string, tty bool, cols, rows int) (ExecStream, error) {
	if !containerRe.MatchString(id) {
		return nil, ErrNoContainer
	}
	if !tty {
		return nil, errors.New("non-tty exec is not supported by this runtime")
	}
	if len(cmd) == 0 {
		return nil, errors.New("exec needs a command")
	}
	body, _ := json.Marshal(map[string]any{"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true,
		"Cmd": cmd, "Env": []string{"TERM=xterm-256color"}})
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/containers/"+id+"/exec", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	var created struct{ Id string }
	err = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 || err != nil || !containerRe.MatchString(created.Id) {
		return nil, fmt.Errorf("docker exec create: %s", resp.Status)
	}
	conn, br, err := d.hijack(ctx, "/exec/"+created.Id+"/start", []byte(`{"Detach":false,"Tty":true}`))
	if err != nil {
		return nil, err
	}
	es := &dockerExec{d: d, id: created.Id, conn: conn, br: br}
	if cols > 0 && rows > 0 {
		_ = es.Resize(cols, rows) // best effort: the first browser resize frame corrects it anyway
	}
	return es, nil
}

func (d *Docker) hijack(ctx context.Context, path string, body []byte) (net.Conn, *bufio.Reader, error) {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", d.Socket)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker"+path, bytes.NewReader(body))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		conn.Close()
		return nil, nil, fmt.Errorf("docker exec start: %s %s", resp.Status, bytes.TrimSpace(b))
	}
	return conn, br, nil
}

type dockerExec struct {
	d    *Docker
	id   string
	conn net.Conn
	br   *bufio.Reader
	once sync.Once
}

func (e *dockerExec) Stdin() io.WriteCloser { return stdin{e} }
func (e *dockerExec) Stdout() io.Reader     { return e.br }

type stdin struct{ e *dockerExec }

func (s stdin) Write(b []byte) (int, error) { return s.e.conn.Write(b) }
func (s stdin) Close() error {
	if uc, ok := s.e.conn.(*net.UnixConn); ok {
		return uc.CloseWrite()
	}
	return nil
}

func (e *dockerExec) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 1000 || rows > 1000 {
		return errors.New("invalid size")
	}
	// The client has no overall timeout because the log stream is long-lived, so bound this call itself: a hung
	// daemon must not stall the browser's input goroutine forever.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://docker/exec/%s/resize?h=%d&w=%d", e.id, rows, cols), nil)
	if err != nil {
		return err
	}
	resp, err := e.d.hc.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("exec resize: %s", resp.Status)
	}
	return nil
}

// Wait polls the exec until it stops and returns its exit code.
func (e *dockerExec) Wait(ctx context.Context) (int, error) {
	t := time.NewTicker(150 * time.Millisecond)
	defer t.Stop()
	for {
		var st struct {
			Running  bool
			ExitCode int
		}
		if err := e.d.get(ctx, "/exec/"+e.id+"/json", &st); err != nil {
			return -1, err
		}
		if !st.Running {
			return st.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-t.C:
		}
	}
}

// ttyEOF is the terminal's own "this session is over" gesture, sent in band before hanging up: ^U discards
// whatever the user had half-typed so teardown cannot execute a partial command, then ^D twice, which makes a
// POSIX shell read end of input and exit.
const ttyEOF = "\x15\x04\x04"

// Close ends the session and drops the attach connection.
//
// Hanging the connection up is not enough, and none of the alternatives were assumed — they were measured against
// Docker 29.1.3:
//
//   - closing the attach socket left /bin/sh running in the container, because the daemon keeps the pty master
//     open. Every closed browser tab would have leaked a live shell;
//   - half-closing the write side does not help either: a tty exec has no stdin pipe for EOF to travel on;
//   - POST /exec/{id}/kill is not registered on this daemon at any API version — /exec/{id}/resize on the same
//     prefix answers 200 and the kill body is the router's own "page not found", so the route is genuinely absent.
//
// So Close sends the terminal's EOF gesture first, which is what actually ends a shell, then asks the daemon to
// kill the exec for daemons and commands that do support it, and finally drops the connection. Safe to call more
// than once.
func (e *dockerExec) Close() error {
	var err error
	e.once.Do(func() {
		_, _ = e.conn.Write([]byte(ttyEOF))
		if uc, ok := e.conn.(*net.UnixConn); ok {
			_ = uc.CloseWrite()
		}
		err = e.kill()
		_ = e.conn.Close()
	})
	return err
}

func (e *dockerExec) kill() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://docker/exec/"+e.id+"/kill", nil)
	if err != nil {
		return err
	}
	resp, err := e.d.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusConflict:
		return nil // stopped, or already stopped: there is nothing left to kill
	case http.StatusNotFound:
		// Measured on Docker 29.1.3 (API 1.52): this daemon does not register the kill route at any API version,
		// while /exec/{id}/resize on the same prefix answers 200. The in-band EOF above is what ends a shell here,
		// so a missing route is tolerated. It is only tolerated because that write happened: without a way to end
		// the session this would be a leak, not a no-op.
		if _, ok := e.conn.(*net.UnixConn); !ok {
			return fmt.Errorf("docker exec kill: %s, and the attach cannot be half-closed on this transport", resp.Status)
		}
		return nil
	default:
		return fmt.Errorf("docker exec kill: %s", resp.Status)
	}
}
