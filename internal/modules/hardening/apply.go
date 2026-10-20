package hardening

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Apply runs one Tier 1 plan: snapshot -> pre commands -> write files -> post commands. Any failure restores the
// snapshot. Writing /etc requires the process to hold root (see PROGRESS.md: the privilege model is still open).
// Returns the snapshot directory.
func (p *Planner) Apply(ctx context.Context, pl Plan, revertDir, changeID string) (string, error) {
	if pl.Tier != 1 {
		return "", ErrTierBlocked
	}
	var paths []string
	for _, f := range pl.Files {
		paths = append(paths, f.Path)
	}
	snap, err := WriteSnapshot(p.Env.Root, revertDir, changeID, paths)
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	fail := func(stage string, err error) (string, error) {
		_ = Revert(p.Env.Root, revertDir, changeID) // restore files; installed packages stay installed
		return snap, fmt.Errorf("%s: %w", stage, err)
	}
	for _, c := range pl.Pre {
		if out, err := p.Run(ctx, c.Name, c.Args...); err != nil {
			return fail(c.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(out)))
		}
	}
	for _, f := range pl.Files {
		real := filepath.Join(p.Env.Root, filepath.FromSlash(f.Path))
		if cur, err := os.ReadFile(real); err == nil && string(cur) == f.Content {
			continue // idempotent: identical content is left untouched
		}
		if err := writeAtomic(real, []byte(f.Content), f.Mode); err != nil {
			return fail("write "+f.Path, err)
		}
	}
	for _, c := range pl.Post {
		if out, err := p.Run(ctx, c.Name, c.Args...); err != nil {
			return fail(c.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(out)))
		}
	}
	return snap, nil
}
