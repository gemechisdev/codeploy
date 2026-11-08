package terminal

import (
	"errors"
	"io"
	"os"
	"syscall"
)

// HelperHint turns the low-level reason a host shell could not start into the one thing the operator should check.
// "Not installed or not running" covers four different faults on the host, and each is fixed differently, so the
// panel names the likely one instead of leaving the operator to guess. It returns "" when the cause is unknown.
func HelperHint(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "the helper socket /run/codeplo-pty/pty.sock does not exist. Run: sudo systemctl enable --now codeplo-pty.socket"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "the socket exists but nothing is listening. Run: sudo systemctl status codeplo-pty.socket"
	case errors.Is(err, os.ErrPermission):
		return "the panel user may not open the helper socket (it must belong to group codeplo). Check: ls -l /run/codeplo-pty/pty.sock"
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "the helper accepted the connection and closed it before answering. Run: sudo journalctl -u 'codeplo-pty@*' -n 50"
	}
	return ""
}
