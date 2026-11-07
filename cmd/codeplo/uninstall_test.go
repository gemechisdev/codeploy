package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeHost struct {
	calls []string
	users map[string]bool
}

func (h *fakeHost) run(name string, args ...string) (string, error) {
	h.calls = append(h.calls, name+" "+strings.Join(args, " "))
	switch name {
	case "id":
		if h.users[args[len(args)-1]] {
			return "1001", nil
		}
		return "", fmt.Errorf("no such user")
	case "systemctl":
		if len(args) > 0 && args[0] == "is-active" {
			return "inactive", fmt.Errorf("inactive")
		}
	}
	return "", nil
}

func (h *fakeHost) called(prefix string) bool {
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

type rig struct {
	u    *uninstaller
	h    *fakeHost
	out  *bytes.Buffer
	root string
	data string
}

func newRig(t *testing.T, o uninstallOpts) *rig {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "var", "lib", "codeplo")
	for _, d := range []string{data, filepath.Join(root, "systemd"), filepath.Join(root, "bin"), filepath.Join(root, "etc")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{
		filepath.Join(root, "systemd", "codeplod.service"),
		filepath.Join(root, "bin", "codeplod"),
		filepath.Join(root, "bin", "codeplo"),
		filepath.Join(data, "codeplo.db"),
		filepath.Join(root, "envfile"),
	} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &fakeHost{users: map[string]bool{"codeplo": true, "codeplo-ops": true}}
	o.yes, o.dataDir = true, data
	out := &bytes.Buffer{}
	u := &uninstaller{
		opts: o, out: out, in: strings.NewReader(""), run: h.run, has: func(string) bool { return true },
		self: "/usr/local/bin/codeplo", systemd: filepath.Join(root, "systemd"), bin: filepath.Join(root, "bin"),
		etc: filepath.Join(root, "etc", "codeplo"), envFile: filepath.Join(root, "envfile"),
	}
	return &rig{u: u, h: h, out: out, root: root, data: data}
}

func (r *rig) markOperator(t *testing.T, created bool) {
	t.Helper()
	dir := filepath.Join(r.data, "operator")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "username"), []byte("codeplo-ops\n"), 0o644)
	if created {
		_ = os.WriteFile(filepath.Join(dir, "created"), []byte("1\n"), 0o644)
	}
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestUninstallRemovesEverythingTheInstallerMade(t *testing.T) {
	r := newRig(t, uninstallOpts{})
	r.markOperator(t, true)
	if err := r.u.execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, r.out)
	}
	for _, p := range []string{r.data, filepath.Join(r.root, "systemd", "codeplod.service"), filepath.Join(r.root, "bin", "codeplod"), filepath.Join(r.root, "bin", "codeplo")} {
		if exists(p) {
			t.Errorf("%s still exists", p)
		}
	}
	if !r.h.called("userdel -r codeplo-ops") || !r.h.called("userdel codeplo") {
		t.Errorf("users not deleted: %v", r.h.calls)
	}
	if !r.h.called("systemctl disable --now") || !r.h.called("systemctl daemon-reload") {
		t.Errorf("systemd not handled: %v", r.h.calls)
	}
	if r.h.called("docker") {
		t.Errorf("workloads must be left alone by default: %v", r.h.calls)
	}
}

func TestUninstallNeverDeletesAnAccountItDidNotCreate(t *testing.T) {
	r := newRig(t, uninstallOpts{})
	r.markOperator(t, false) // the user existed before the installer ran
	if err := r.u.execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, r.out)
	}
	if r.h.called("userdel -r") {
		t.Errorf("deleted a pre-existing operator account: %v", r.h.calls)
	}
}

func TestUninstallKeepDataKeepsUsersAndData(t *testing.T) {
	r := newRig(t, uninstallOpts{keepData: true})
	r.markOperator(t, true)
	if err := r.u.execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, r.out)
	}
	if !exists(filepath.Join(r.data, "codeplo.db")) {
		t.Error("data was deleted despite --keep-data")
	}
	if r.h.called("userdel") {
		t.Errorf("users deleted despite --keep-data: %v", r.h.calls)
	}
	if exists(filepath.Join(r.root, "bin", "codeplod")) {
		t.Error("binaries should still be removed")
	}
}

func TestUninstallDryRunChangesNothing(t *testing.T) {
	r := newRig(t, uninstallOpts{dry: true})
	r.markOperator(t, true)
	if err := r.u.execute(); err != nil {
		t.Fatal(err)
	}
	if !exists(r.data) || !exists(filepath.Join(r.root, "bin", "codeplo")) || len(r.h.calls) > 3 {
		t.Errorf("dry run changed the host: %v", r.h.calls)
	}
}

func TestUninstallAbortsWithoutConfirmation(t *testing.T) {
	r := newRig(t, uninstallOpts{})
	r.u.opts.yes = false
	r.u.in = strings.NewReader("no\n")
	if err := r.u.execute(); err == nil || !exists(r.data) {
		t.Fatalf("expected abort, err=%v", err)
	}
	r2 := newRig(t, uninstallOpts{})
	r2.u.opts.yes = false
	r2.u.in = strings.NewReader("uninstall\n")
	if err := r2.u.execute(); err != nil || exists(r2.data) {
		t.Fatalf("typed confirmation should proceed, err=%v", err)
	}
}

func TestUninstallRemoveWorkloadsAndPurgeConfig(t *testing.T) {
	r := newRig(t, uninstallOpts{workloads: true, purgeConfig: true})
	if err := r.u.execute(); err != nil {
		t.Fatal(err)
	}
	if !r.h.called("docker ps -aq --filter label=codeplo.managed=true") {
		t.Errorf("workloads not listed: %v", r.h.calls)
	}
	if exists(filepath.Join(r.root, "envfile")) {
		t.Error("env file should be purged")
	}
}

func TestSafeDataDir(t *testing.T) {
	for p, ok := range map[string]bool{
		"/var/lib/codeplo": true, "/srv/codeplo-data": true,
		"/": false, "/var": false, "/home": false, "/var/lib": false, "relative/codeplo": false, "/var/lib/other": false,
	} {
		if err := safeDataDir(p); (err == nil) != ok {
			t.Errorf("safeDataDir(%q) = %v, want ok=%v", p, err, ok)
		}
	}
}

func TestDataDirFromUnit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "codeplod.service")
	_ = os.WriteFile(p, []byte("[Service]\nUser=codeplo\nEnvironment=CODEPLO_DATA_DIR=/srv/codeplo\n"), 0o644)
	if got := dataDirFromUnit(p); got != "/srv/codeplo" {
		t.Errorf("got %q", got)
	}
	if got := dataDirFromUnit(p + ".missing"); got != "" {
		t.Errorf("missing unit should give empty, got %q", got)
	}
}

func TestPendingRevert(t *testing.T) {
	d := t.TempDir()
	if pendingRevert(d) {
		t.Error("empty dir has no pending revert")
	}
	_ = os.MkdirAll(filepath.Join(d, "revert", "chg1"), 0o755)
	_ = os.WriteFile(filepath.Join(d, "revert", "chg1", "manifest.json"), []byte("{}"), 0o644)
	if !pendingRevert(d) {
		t.Error("manifest present, expected pending")
	}
}
