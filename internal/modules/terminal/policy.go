package terminal

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// HelperRequest is the handshake line the daemon sends to the privileged PTY helper.
type HelperRequest struct {
	User string `json:"user"`
	Root bool   `json:"root"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type HelperReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Account is a validated login target.
type Account struct {
	Name, Home, Shell string
	UID, GID          int
}

var (
	ErrRootDisabled = errors.New("root shell is not enabled on this host")
	ErrBadUser      = errors.New("user is not an eligible operator account")
)

// Authorize decides, on the root side of the privilege boundary, whether the helper may start a shell. The daemon
// is unprivileged and is not trusted to pick arbitrary accounts: the helper accepts only (a) root, and only when
// the host owner created the root-owned flag file allowRootFile, or (b) a normal login user (uid 1000..65533, real
// shell) that is a member of sudo or wheel, the same predicate the setup wizard uses for operator candidates.
func Authorize(req HelperRequest, passwdPath, groupPath, allowRootFile string) (Account, error) {
	if req.Root {
		if _, err := os.Stat(allowRootFile); err != nil {
			return Account{}, ErrRootDisabled
		}
		return Account{Name: "root", Home: "/root", Shell: "/bin/bash", UID: 0, GID: 0}, nil
	}
	if !userRe.MatchString(req.User) {
		return Account{}, ErrBadUser
	}
	sudoers, sudoGIDs := map[string]bool{}, map[string]bool{}
	if b, err := os.ReadFile(groupPath); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Split(ln, ":")
			if len(f) >= 4 && (f[0] == "sudo" || f[0] == "wheel") {
				sudoGIDs[f[2]] = true
				for _, m := range strings.Split(f[3], ",") {
					sudoers[strings.TrimSpace(m)] = true
				}
			}
		}
	}
	b, err := os.ReadFile(passwdPath)
	if err != nil {
		return Account{}, fmt.Errorf("read passwd: %w", err)
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Split(ln, ":")
		if len(f) < 7 || f[0] != req.User {
			continue
		}
		uid, e1 := strconv.Atoi(f[2])
		gid, e2 := strconv.Atoi(f[3])
		if e1 != nil || e2 != nil || uid < 1000 || uid >= 65534 || f[6] == "" ||
			strings.HasSuffix(f[6], "nologin") || strings.HasSuffix(f[6], "/false") {
			return Account{}, ErrBadUser
		}
		if !sudoers[f[0]] && !sudoGIDs[f[3]] {
			return Account{}, ErrBadUser
		}
		return Account{Name: f[0], Home: f[5], Shell: f[6], UID: uid, GID: gid}, nil
	}
	return Account{}, ErrBadUser
}

// ClampSize bounds a tty size to something a terminal can sensibly have.
func ClampSize(cols, rows int) (int, int) {
	c := func(v, lo, hi, def int) int {
		if v < lo || v > hi {
			return def
		}
		return v
	}
	return c(cols, 10, 500, 80), c(rows, 2, 200, 24)
}
