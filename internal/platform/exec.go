package platform

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Run executes name with args (never through a shell) and returns combined output.
func Run(ctx context.Context, timeout time.Duration, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Runner is the injectable command runner used by drivers, preflight and the edge supervisor.
type Runner func(ctx context.Context, name string, args ...string) (string, error)

// DefaultRunner runs real commands with a 60 s ceiling.
func DefaultRunner(ctx context.Context, name string, args ...string) (string, error) {
	return Run(ctx, 60*time.Second, name, args...)
}

// DistroID returns the ID= field of /etc/os-release (for example "ubuntu", "debian"), or "" when unknown.
func DistroID() string { return distroFrom("/etc/os-release") }

func distroFrom(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "ID=") {
			return strings.Trim(strings.TrimPrefix(l, "ID="), `"' `)
		}
	}
	return ""
}

// SupportedDistro reports whether the apt-based drivers apply (Debian and Ubuntu only in v1).
func SupportedDistro(id string) bool { return id == "debian" || id == "ubuntu" }

// SystemdActive reports whether `systemctl is-active unit` succeeds (works unprivileged).
func SystemdActive(ctx context.Context, run Runner, unit string) bool {
	out, err := run(ctx, "systemctl", "is-active", unit)
	return err == nil && strings.TrimSpace(out) == "active"
}
