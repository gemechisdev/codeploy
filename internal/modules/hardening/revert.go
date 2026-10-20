package hardening

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"context"

	"github.com/codeplo/codeplo/internal/platform"
)

// DefaultRevertDir holds snapshots; each change gets /var/lib/codeplo/revert/<id>/.
const DefaultRevertDir = "/var/lib/codeplo/revert"

// Spool layout: <revertDir>/<changeID>/manifest.json is the file state, <changeID>/revert.json (optional) the
// live state to restore. Both names are constants because the CLI revert reads them without the daemon.
const (
	SnapshotManifest   = "manifest.json"
	RevertCommandsFile = "revert.json"
)

type snapFile struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Mode    uint32 `json:"mode"`
	Blob    string `json:"blob,omitempty"`
}

type manifest struct {
	ID      string     `json:"id"`
	Created string     `json:"created"`
	Files   []snapFile `json:"files"`
}

// WriteSnapshot records the current state of every path before a change. Absolute host paths are stored; root
// is a prefix used by tests ("/" in production).
func WriteSnapshot(root, revertDir, id string, paths []string) (string, error) {
	dir := filepath.Join(revertDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	m := manifest{ID: id, Created: time.Now().UTC().Format(time.RFC3339)}
	for i, p := range paths {
		sf := snapFile{Path: p}
		real := filepath.Join(root, filepath.FromSlash(p))
		if st, err := os.Stat(real); err == nil && st.Mode().IsRegular() {
			b, err := os.ReadFile(real)
			if err != nil {
				return "", err
			}
			sf.Existed, sf.Mode, sf.Blob = true, uint32(st.Mode().Perm()), "blob"+strconv.Itoa(i)
			if err := os.WriteFile(filepath.Join(dir, sf.Blob), b, 0o600); err != nil {
				return "", err
			}
		}
		m.Files = append(m.Files, sf)
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	return dir, os.WriteFile(filepath.Join(dir, SnapshotManifest), b, 0o600)
}

// Revert restores a snapshot. It needs neither the daemon, the database nor the network (plan 3.10): it is the
// body of `codeplo revert <id>` and of the systemd transient timer.
func Revert(root, revertDir, id string) error {
	dir := filepath.Join(revertDir, id)
	raw, err := os.ReadFile(filepath.Join(dir, SnapshotManifest))
	if err != nil {
		return fmt.Errorf("read snapshot %s: %w", id, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	var firstErr error
	for _, f := range m.Files {
		real := filepath.Join(root, filepath.FromSlash(f.Path))
		var e error
		if f.Existed {
			var b []byte
			if b, e = os.ReadFile(filepath.Join(dir, f.Blob)); e == nil {
				e = writeAtomic(real, b, os.FileMode(f.Mode))
			}
		} else if err := os.Remove(real); err != nil && !os.IsNotExist(err) {
			e = err
		}
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	return firstErr
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".codeplo-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ScheduleRevertTimer arms the daemon-independent dead-man switch: a systemd transient timer that runs
// `<bin> revert <id>`. Used by Tier 2/3 apply (P2b/P2c); Tier 1 cannot cut access and does not arm it.
func ScheduleRevertTimer(ctx context.Context, run platform.Runner, bin, id string, after time.Duration) (string, error) {
	unit := "codeplo-revert-" + id
	_, err := run(ctx, "systemd-run", "--unit="+unit, "--on-active="+strconv.Itoa(int(after.Seconds()))+"s", bin, "revert", id)
	return unit, err
}

// RuntimeStep is one idempotent restore of live kernel state recorded in a snapshot: Probe reads whether the
// object is still there, Apply removes it. A failing Probe means there is nothing left to restore and is not an
// error, so replaying a revert twice is a no-op. Both commands are argv, never a shell.
type RuntimeStep struct {
	Probe Cmd `json:"probe"`
	Apply Cmd `json:"apply"`
}

// WriteRuntimeRevert records the live-state steps RuntimeRevert replays, into the snapshot Apply just wrote.
func WriteRuntimeRevert(revertDir, id string, steps []RuntimeStep) error {
	b, err := json.MarshalIndent(steps, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(revertDir, id, RevertCommandsFile), b, 0o600)
}

// PlanRuntimeRevert records, for the snapshot of one applied plan, the live-state restore the CLI revert needs.
// Tier 1 plans record nothing: restoring their files is the whole revert.
func PlanRuntimeRevert(revertDir, id string, pl Plan) error {
	var steps []RuntimeStep
	switch pl.CheckKey {
	case "fw.default_deny":
		steps = NftRuntimeRevert()
	case "ssh.no_password", "ssh.no_root_password", "ssh.limit_auth":
		steps = SSHRuntimeRevert(platform.DistroID())
	default:
		return nil
	}
	return WriteRuntimeRevert(revertDir, id, steps)
}

// NftRuntimeRevert removes only codeplo's own table, after the snapshot has restored the files. The probe makes
// the step safe when the table was never loaded or an earlier revert already deleted it.
func NftRuntimeRevert() []RuntimeStep {
	t := []string{"table", "inet", NftTableName}
	return []RuntimeStep{{
		Probe: Cmd{"nft", append([]string{"list"}, t...)},
		Apply: Cmd{"nft", append([]string{"delete"}, t...)},
	}}
}

// SSHRuntimeRevert validates the restored sshd configuration and reloads the daemon (reload, not restart: the
// operator's session must survive the revert).
func SSHRuntimeRevert(distro string) []RuntimeStep {
	return []RuntimeStep{{
		Probe: Cmd{"sshd", []string{"-t"}},
		Apply: Cmd{"systemctl", []string{"reload", sshdUnitFor(distro)}},
	}}
}

// RuntimeRevert replays the live-state steps recorded for one snapshot. A snapshot without revert.json had no
// live state; a snapshot with no manifest is refused by Revert, so nothing is ever guessed at.
func RuntimeRevert(ctx context.Context, revertDir, id string, run platform.Runner) error {
	raw, err := os.ReadFile(filepath.Join(revertDir, id, RevertCommandsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s for %s: %w", RevertCommandsFile, id, err)
	}
	var steps []RuntimeStep
	if err := json.Unmarshal(raw, &steps); err != nil {
		return fmt.Errorf("parse %s for %s: %w", RevertCommandsFile, id, err)
	}
	if run == nil {
		return fmt.Errorf("snapshot %s records live-state steps but no command runner was given", id)
	}
	for _, s := range steps {
		if _, err := run(ctx, s.Probe.Name, s.Probe.Args...); err != nil {
			continue // already gone: nothing to restore, and no destructive guess
		}
		if out, err := run(ctx, s.Apply.Name, s.Apply.Args...); err != nil {
			return fmt.Errorf("%s: %v: %s", s.Apply.String(), err, strings.TrimSpace(out))
		}
	}
	return nil
}
