package git

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// CloneLimits bounds one clone. The values come from settings (seeded by 0005_git.sql) so an operator with a
// large repository can widen them without a rebuild, and so a repository that hangs the network cannot hold a
// deployment worker open until the whole build budget is gone.
type CloneLimits struct {
	Timeout  time.Duration
	MaxBytes int64
}

const (
	keyCloneTimeout = "git.clone_timeout_s"
	keyCloneMaxMB   = "git.max_clone_mb"
)

// ReadCloneLimits reads the two settings with the seeded defaults as the floor. A setting below one second is
// ignored rather than honoured: a clone that is allowed to run for no time at all is a misconfiguration, not a
// policy.
//
// It is a package function because the deployment engine, not the git service, is what clones: both sides need the
// same numbers, and two copies of the clamping rules is how a limit stops meaning anything.
func ReadCloneLimits(ctx context.Context, q Querier) CloneLimits {
	return CloneLimits{
		Timeout:  time.Duration(clamp(settingInt(ctx, q, keyCloneTimeout, 300), 1, 3600)) * time.Second,
		MaxBytes: int64(clamp(settingInt(ctx, q, keyCloneMaxMB, 2048), 16, 262144)) * 1024 * 1024,
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func settingInt(ctx context.Context, q Querier, key string, def int) int {
	var raw string
	if err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&raw); err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return n
}

// SourceCheck is the outcome of verifying a source before it is stored: what the panel shows when the operator
// saves a configuration that could never build.
type SourceCheck struct {
	OK      bool
	Message string
}

// VerifySource is the pre-flight a saved source must pass. The API's shape validation says whether the request is
// well formed; this says whether the deployment engine could act on it: a deploy-key source with no sealed key,
// or a key that will not parse, is a build that fails a minute later with a git error nobody can read.
//
// It is called with the caller's transaction, so it can see the key written in the same request.
func (s *Service) VerifySource(ctx context.Context, q Querier, resourceID string, src Source) SourceCheck {
	if src.Kind == KindRegistry {
		return SourceCheck{OK: true}
	}
	if src.RepoURL == nil || !gitURLRe.MatchString(deref(src.RepoURL)) {
		return SourceCheck{Message: "The repository URL cannot be cloned. Use git@host:org/repo.git or https://host/org/repo."}
	}
	if src.Branch == nil || !branchRe.MatchString(deref(src.Branch)) {
		return SourceCheck{Message: "The branch name cannot be checked out."}
	}
	if src.Kind != KindDeployKey {
		return SourceCheck{OK: true}
	}
	key, absent, err := s.OpenCredential(ctx, q, resourceID, KindDeployKeyCred)
	if err != nil && errors.Is(err, ErrSealedKey) {
		return SourceCheck{Message: "The stored deploy key cannot be opened with this daemon's master key. Generate a new one."}
	}
	if err != nil {
		return SourceCheck{Message: "The stored deploy key could not be read."}
	}
	if absent {
		return SourceCheck{Message: "Generate a deploy key first"}
	}
	if _, err := ssh.ParsePrivateKey(key); err != nil {
		return SourceCheck{Message: "The stored deploy key is not a usable private key. Generate a new one."}
	}
	return SourceCheck{OK: true}
}

// UnreachableSourceError formats the pre-flight failure the same way every other 422 on this endpoint does.
func UnreachableSourceError(msg string) error {
	return FieldErrors{"source": fmt.Sprintf("This source cannot be built: %s", msg)}
}
