package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Operator is a host account that may own the host terminal (plan 3.9: named, non-root).
type Operator struct {
	Username          string `json:"username"`
	UID               int    `json:"uid"`
	Sudo              bool   `json:"sudo"`
	HasAuthorizedKeys bool   `json:"has_authorized_keys"`
}

var UsernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// OperatorCandidates lists login-capable, non-system users with their sudo and authorized_keys status.
// It only reads files: no command runs. has_authorized_keys is false when the daemon user cannot traverse the
// home directory (the usual case under the unprivileged service user); the P2 hardening helper re-checks as root.
func OperatorCandidates() []Operator {
	return operatorsFrom("/etc/passwd", "/etc/group")
}

func operatorsFrom(passwdPath, groupPath string) []Operator {
	sudoers := map[string]bool{}
	sudoGID := map[string]bool{}
	if b, err := os.ReadFile(groupPath); err == nil {
		for _, ln := range strings.Split(string(b), "\n") {
			f := strings.Split(ln, ":")
			if len(f) < 4 || (f[0] != "sudo" && f[0] != "wheel") {
				continue
			}
			sudoGID[f[2]] = true
			for _, m := range strings.Split(f[3], ",") {
				if m = strings.TrimSpace(m); m != "" {
					sudoers[m] = true
				}
			}
		}
	}
	out := []Operator{}
	b, err := os.ReadFile(passwdPath)
	if err != nil {
		return out
	}
	for _, ln := range strings.Split(string(b), "\n") {
		f := strings.Split(ln, ":")
		if len(f) < 7 {
			continue
		}
		uid, err := strconv.Atoi(f[2])
		if err != nil || uid < 1000 || uid >= 65534 || !UsernameRe.MatchString(f[0]) {
			continue
		}
		if sh := f[6]; strings.HasSuffix(sh, "nologin") || strings.HasSuffix(sh, "/false") || sh == "" {
			continue
		}
		o := Operator{Username: f[0], UID: uid, Sudo: sudoers[f[0]] || sudoGID[f[3]]}
		if st, err := os.Stat(filepath.Join(f[5], ".ssh", "authorized_keys")); err == nil && st.Size() > 0 {
			o.HasAuthorizedKeys = true
		}
		out = append(out, o)
	}
	return out
}
