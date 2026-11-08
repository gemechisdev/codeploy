// Package builders turns a source into an image: Git clone (deploy key or token), registry pull, and Dockerfile
// build through BuildKit. It shells out to git and the docker CLI (never through a shell), because BuildKit secret
// mounts need the CLI's build session. Credentials only ever exist as 0600 files inside 0700 temp directories
// that are removed when the call returns; none is placed on a command line or in a URL.
package builders

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LogFunc receives one output line. stream is "stdout", "stderr" or "system".
type LogFunc func(stream, line string)

// RegistryAuth are registry credentials for one server.
type RegistryAuth struct{ Server, Username, Password string }

// GitSource is a repository to clone. Exactly one of DeployKey (PEM) or Token may be set; both empty clones publicly.
// MaxBytes bounds the clone (0 means unbounded); the caller sets it from the git.max_clone_mb setting.
type GitSource struct {
	URL, Branch string
	DeployKey   []byte
	Token       string
	MaxBytes    int64
}

type CloneResult struct{ Commit, Message string }

type BuildRequest struct {
	ContextDir  string            // already confined to the clone directory
	Dockerfile  string            // absolute path, already confined
	Tag         string            // image tag to produce
	Secrets     map[string]string // BuildKit secret mounts: id -> value (never ARG)
	Args        map[string]string // non-secret build variables only (--build-arg)
	MemoryBytes int64             // build memory limit (<= 50% of available RAM, chosen by preflight)
	Auth        *RegistryAuth     // for private base images
}

type PullRequest struct {
	Ref  string
	Auth *RegistryAuth
}

// Builder is the engine's port to the outside world of sources and builds. The CLI implementation is real; tests
// substitute a fake.
type Builder interface {
	Clone(ctx context.Context, src GitSource, dest string, log LogFunc) (CloneResult, error)
	Build(ctx context.Context, req BuildRequest, log LogFunc) error
	Pull(ctx context.Context, req PullRequest, log LogFunc) error
	// ComposeUp starts a docker compose project (multi-service). Optional for fakes: return an error if unsupported.
	ComposeUp(ctx context.Context, project, dir, composeFile string, log LogFunc) error
}

var (
	// ErrOutOfMemory and ErrNoSpace classify failures the engine reports with an actionable message.
	ErrOutOfMemory = errors.New("the build was killed: it ran out of memory")
	ErrNoSpace     = errors.New("no space left on device")
	// ErrSourceTooLarge is a clone that finished but does not fit the configured budget: a repository nobody
	// checked, not a broken host.
	ErrSourceTooLarge = errors.New("the repository is larger than the clone limit")

	secretIDRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)
	gitURLRe   = regexp.MustCompile(`^(https://[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._~/%+-]+|git@[A-Za-z0-9.-]+:[A-Za-z0-9._~/+-]+|ssh://git@[A-Za-z0-9.-]+(:[0-9]+)?/[A-Za-z0-9._~/+-]+)$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
	refRe      = regexp.MustCompile(`^[a-z0-9][a-z0-9._/:-]{0,200}(@sha256:[a-f0-9]{64})?$`)
)

// ValidateGitURL rejects anything that is not a plain https/ssh URL without embedded credentials. Credentials in
// URLs leak into process lists, logs and .git/config, so they are refused outright.
func ValidateGitURL(u string) error {
	if strings.HasPrefix(u, "-") || strings.Contains(u, "@") && strings.HasPrefix(u, "https://") || !gitURLRe.MatchString(u) {
		return fmt.Errorf("repository URL must be https://host/path or git@host:path without embedded credentials")
	}
	return nil
}

// ConfinePath joins rel onto root, resolves symlinks and requires the result to stay inside root. Build contexts
// and Dockerfile paths go through it, so a repository symlink pointing at /etc cannot be used as a context.
func ConfinePath(root, rel string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the repository", rel)
	}
	joined := filepath.Join(realRoot, filepath.Clean("/"+rel))
	real, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", err
	}
	if real != realRoot && !strings.HasPrefix(real, realRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the repository", rel)
	}
	return real, nil
}
