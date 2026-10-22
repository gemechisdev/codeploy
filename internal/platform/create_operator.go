package platform

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// CreateOperator creates a login user with sudo and an authorized_keys entry.
// It must run as root (setup/installer path). Public key is a single OpenSSH public key line.
func CreateOperator(ctx context.Context, username, publicKey string) (Operator, error) {
	if !UsernameRe.MatchString(username) {
		return Operator{}, fmt.Errorf("invalid username")
	}
	key := strings.TrimSpace(publicKey)
	if err := validateSSHPublicKey(key); err != nil {
		return Operator{}, err
	}
	if os.Geteuid() != 0 {
		return Operator{}, fmt.Errorf("creating an operator requires root")
	}
	if _, err := user.Lookup(username); err == nil {
		return Operator{}, fmt.Errorf("user already exists")
	}

	run := func(name string, args ...string) error {
		out, err := Run(ctx, 30*time.Second, name, args...)
		if err != nil {
			return fmt.Errorf("%s %v: %w (%s)", name, args, err, strings.TrimSpace(out))
		}
		return nil
	}
	// -m home, -s login shell. Group membership is added next so the account is not half-sudo on failure mid-way.
	if err := run("useradd", "-m", "-s", "/bin/bash", username); err != nil {
		return Operator{}, err
	}
	// Prefer sudo (Debian/Ubuntu); fall back to wheel (RHEL-like). Failure here still leaves a usable login user.
	if err := run("usermod", "-aG", "sudo", username); err != nil {
		_ = run("usermod", "-aG", "wheel", username)
	}

	u, err := user.Lookup(username)
	if err != nil {
		return Operator{}, fmt.Errorf("lookup after create: %w", err)
	}
	sshDir := filepath.Join(u.HomeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return Operator{}, err
	}
	ak := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(ak, []byte(key+"\n"), 0o600); err != nil {
		return Operator{}, err
	}
	uid := u.Uid
	gid := u.Gid
	// Best-effort ownership; useradd -m should already own the home.
	_ = run("chown", "-R", uid+":"+gid, sshDir)

	uidN := 0
	fmt.Sscanf(u.Uid, "%d", &uidN)
	return Operator{
		Username:          username,
		UID:               uidN,
		Sudo:              true,
		HasAuthorizedKeys: true,
	}, nil
}

func validateSSHPublicKey(key string) error {
	if key == "" {
		return fmt.Errorf("public key is required")
	}
	if strings.ContainsAny(key, "\r\n") {
		return fmt.Errorf("public key must be a single line")
	}
	parts := strings.Fields(key)
	if len(parts) < 2 {
		return fmt.Errorf("public key looks incomplete")
	}
	switch parts[0] {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "ssh-dss":
		return nil
	default:
		return fmt.Errorf("unsupported key type %q", parts[0])
	}
}
