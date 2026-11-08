package terminal

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelperHintNamesTheFault(t *testing.T) {
	dir := t.TempDir()
	_, missing := net.Dial("unix", filepath.Join(dir, "none.sock"))
	if h := HelperHint(missing); !strings.Contains(h, "enable --now codeplo-pty.socket") {
		t.Errorf("missing socket: %q", h)
	}
	// A socket file with no listener behind it is a real ECONNREFUSED.
	p := filepath.Join(dir, "dead.sock")
	l, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	_, refused := net.Dial("unix", p)
	if h := HelperHint(fmt.Errorf("%w: %w", io.ErrClosedPipe, refused)); !strings.Contains(h, "nothing is listening") {
		t.Errorf("refused: %q (err=%v)", h, refused)
	}
	if h := HelperHint(fmt.Errorf("x: %w", os.ErrPermission)); !strings.Contains(h, "group codeplo") {
		t.Errorf("permission: %q", h)
	}
	if h := HelperHint(fmt.Errorf("x: %w", io.EOF)); !strings.Contains(h, "journalctl") {
		t.Errorf("eof: %q", h)
	}
	if HelperHint(fmt.Errorf("something else")) != "" {
		t.Error("unknown cause must give no hint")
	}
}
