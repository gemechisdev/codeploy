//go:build linux

package terminal

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// HelperPolicy locates the files the helper consults. Defaults are the real host paths.
type HelperPolicy struct {
	Passwd, Group, AllowRootFile, ServiceUser string
}

func DefaultHelperPolicy() HelperPolicy {
	return HelperPolicy{Passwd: "/etc/passwd", Group: "/etc/group", AllowRootFile: "/etc/codeplo/allow-root", ServiceUser: "codeplo"}
}

// ServeHelper runs one shell session on conn and returns when it ends. It is started per connection by systemd
// (codeplo-pty@.service, Accept=yes, running as root) through `codeplo pty-helper`. It never trusts the peer:
// the connecting uid must be the service user, and Authorize restricts which accounts can be entered.
func ServeHelper(conn net.Conn, pol HelperPolicy) error {
	defer conn.Close()
	if err := checkPeer(conn, pol.ServiceUser); err != nil {
		return err
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.ReadSlice('\n') // bounded by the 64 KiB buffer
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	conn.SetReadDeadline(time.Time{})
	var req HelperRequest
	if err := json.Unmarshal(line, &req); err != nil {
		return reply(conn, HelperReply{Error: "bad request"})
	}
	acct, err := Authorize(req, pol.Passwd, pol.Group, pol.AllowRootFile)
	if err != nil {
		return reply(conn, HelperReply{Error: err.Error()})
	}
	cols, rows := ClampSize(req.Cols, req.Rows)

	cmd := exec.Command(acct.Shell)
	cmd.Args = []string{"-" + filepath.Base(acct.Shell)} // leading dash: login shell
	cmd.Dir = acct.Home
	if st, err := os.Stat(acct.Home); err != nil || !st.IsDir() {
		cmd.Dir = "/"
	}
	cmd.Env = []string{"HOME=" + acct.Home, "USER=" + acct.Name, "LOGNAME=" + acct.Name, "SHELL=" + acct.Shell,
		"TERM=xterm-256color", "LANG=C.UTF-8", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
	attrs := &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if acct.UID != 0 {
		cred := &syscall.Credential{Uid: uint32(acct.UID), Gid: uint32(acct.GID)}
		if u, err := user.LookupId(strconv.Itoa(acct.UID)); err == nil {
			if gids, err := u.GroupIds(); err == nil {
				for _, g := range gids {
					if n, err := strconv.Atoi(g); err == nil {
						cred.Groups = append(cred.Groups, uint32(n))
					}
				}
			}
		}
		attrs.Credential = cred
	}
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}, attrs)
	if err != nil {
		return reply(conn, HelperReply{Error: "cannot start shell: " + err.Error()})
	}
	defer ptmx.Close()
	if err := reply(conn, HelperReply{OK: true}); err != nil {
		killGroup(cmd)
		return err
	}

	var wmu sync.Mutex
	send := func(t byte, p []byte) error { wmu.Lock(); defer wmu.Unlock(); return WriteFrame(conn, t, p) }

	// socket -> pty. Any read error means the daemon went away or the browser tab closed: kill the process group.
	go func() {
		for {
			t, p, err := ReadFrame(r)
			if err != nil {
				killGroup(cmd)
				return
			}
			switch t {
			case FrameData:
				if _, err := ptmx.Write(p); err != nil {
					return
				}
			case FrameResize:
				if c, rw, err := ParseResize(p); err == nil {
					c, rw = ClampSize(c, rw)
					_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(c), Rows: uint16(rw)})
				}
			}
		}
	}()

	// pty -> socket
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32<<10)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if send(FrameData, buf[:n]) != nil {
					killGroup(cmd)
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	werr := cmd.Wait()
	code := 0
	var ee *exec.ExitError
	if errors.As(werr, &ee) {
		code = ee.ExitCode()
	}
	select { // let the last output drain, but never hang on a stuck reader
	case <-done:
	case <-time.After(time.Second):
	}
	return send(FrameExit, ExitPayload(code))
}

func reply(conn net.Conn, rep HelperReply) error {
	b, _ := json.Marshal(rep)
	_, err := conn.Write(append(b, '\n'))
	return err
}

// killGroup ends the whole session: the shell is a session leader (Setsid), so its pid is the process group id.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	pgid := cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGHUP)
	time.AfterFunc(2*time.Second, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
}

func checkPeer(conn net.Conn, serviceUser string) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("helper requires a unix socket")
	}
	want, err := user.Lookup(serviceUser)
	if err != nil {
		return fmt.Errorf("service user: %w", err)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if serr != nil {
		return serr
	}
	if strconv.Itoa(int(cred.Uid)) != want.Uid {
		return fmt.Errorf("peer uid %d is not %s", cred.Uid, serviceUser)
	}
	return nil
}

// ServeHelperStdin serves the connection systemd handed over as fd 0 (StandardInput=socket).
func ServeHelperStdin(pol HelperPolicy) error {
	f := os.NewFile(0, "systemd-connection")
	conn, err := net.FileConn(f)
	if err != nil {
		return err
	}
	return ServeHelper(conn, pol)
}
