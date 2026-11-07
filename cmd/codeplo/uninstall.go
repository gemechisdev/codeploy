package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// uninstall removes what deploy/install.sh put on the host, in the reverse order, so a host can be returned to a
// clean state (or reinstalled) with one command. It is best effort by design: a failed step is reported and the
// rest still run, because a half-removed panel is worse than a loud list of leftovers.
//
// Scope rules:
//   - Only things the installer creates are removed. The operator user (codeplo-ops) is removed only when the
//     installer wrote the "created" marker, i.e. it made the account itself; an account that already existed is
//     never deleted.
//   - Workload containers, networks and volumes (label codeplo.managed=true) are left alone unless asked for:
//     they belong to the operator's applications, not to the panel.
//   - Hardening (firewall, sshd) that was applied and confirmed is not undone; pending snapshots can be reverted
//     first with --revert-hardening.

const (
	defaultDataDir  = "/var/lib/codeplo"
	systemdDir      = "/etc/systemd/system"
	binDir          = "/usr/local/bin"
	etcCodeploDir   = "/etc/codeplo"
	envFilePath     = "/etc/default/codeplod"
	serviceUser     = "codeplo"
	managedLabel    = "codeplo.managed=true"
	confirmPhrase   = "uninstall"
	operatorMarker  = "username"
	operatorCreated = "created"
)

type uninstallOpts struct {
	yes, dry               bool
	keepData, keepOperator bool
	workloads, purgeConfig bool
	revertHardening, force bool
	dataDir                string
}

type uninstaller struct {
	opts     uninstallOpts
	out      io.Writer
	in       io.Reader
	run      func(name string, args ...string) (string, error)
	has      func(name string) bool
	self     string // path of the running codeplo binary, for the revert step
	systemd  string
	bin      string
	etc      string
	envFile  string
	failures []string
}

func newUninstaller(o uninstallOpts) *uninstaller {
	self, _ := os.Executable()
	return &uninstaller{
		opts: o, out: os.Stdout, in: os.Stdin, self: self,
		systemd: systemdDir, bin: binDir, etc: etcCodeploDir, envFile: envFilePath,
		run: func(name string, args ...string) (string, error) {
			b, err := exec.Command(name, args...).CombinedOutput()
			return strings.TrimSpace(string(b)), err
		},
		has: func(name string) bool { _, err := exec.LookPath(name); return err == nil },
	}
}

func uninstallCommand(args []string) error {
	var o uninstallOpts
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.BoolVar(&o.yes, "yes", false, "do not ask for confirmation")
	fs.BoolVar(&o.yes, "y", false, "alias for --yes")
	fs.BoolVar(&o.dry, "dry-run", false, "print the plan and change nothing")
	fs.BoolVar(&o.keepData, "keep-data", false, "keep /var/lib/codeplo (database, keys) and the codeplo and operator users, so a reinstall resumes")
	fs.BoolVar(&o.keepOperator, "keep-operator", false, "keep the install-time operator user even if the installer created it")
	fs.BoolVar(&o.workloads, "remove-workloads", false, "also remove containers, networks and volumes labelled codeplo.managed=true")
	fs.BoolVar(&o.purgeConfig, "purge-config", false, "also remove /etc/default/codeplod (your CODEPLO_* settings)")
	fs.BoolVar(&o.revertHardening, "revert-hardening", false, "run `codeplo revert all` for pending hardening changes first")
	fs.BoolVar(&o.force, "force", false, "continue even if the hardening revert fails")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory (default: read from the codeplod unit, else CODEPLO_DATA_DIR, else "+defaultDataDir+")")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: sudo codeplo uninstall [--dry-run] [--yes] [--keep-data] [--keep-operator] [--remove-workloads] [--purge-config] [--revert-hardening] [--force] [--data-dir DIR]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if !o.dry && os.Geteuid() != 0 {
		return fmt.Errorf("uninstall must run as root: sudo codeplo uninstall")
	}
	return newUninstaller(o).execute()
}

// dataDirFromUnit reads Environment=CODEPLO_DATA_DIR=... from the installed unit, which is the directory the daemon
// really uses; it must be read before the unit file is deleted.
func dataDirFromUnit(unitPath string) string {
	b, err := os.ReadFile(unitPath)
	if err != nil {
		return ""
	}
	for _, ln := range strings.Split(string(b), "\n") {
		ln = strings.TrimSpace(ln)
		const p = "Environment=CODEPLO_DATA_DIR="
		if strings.HasPrefix(ln, p) {
			return strings.Trim(strings.TrimPrefix(ln, p), `"' `)
		}
	}
	return ""
}

// safeDataDir refuses anything that is not clearly a codeplo data directory, because the next step is a
// recursive delete driven by a flag, an environment variable or a unit file.
func safeDataDir(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("data dir %q is not absolute", p)
	}
	c := filepath.Clean(p)
	if len(strings.Split(strings.Trim(c, "/"), "/")) < 2 {
		return fmt.Errorf("refusing to remove %q: too close to the filesystem root", c)
	}
	if !strings.Contains(filepath.Base(c), "codeplo") {
		return fmt.Errorf("refusing to remove %q: the directory name does not contain \"codeplo\"", c)
	}
	return nil
}

func (u *uninstaller) resolveDataDir() string {
	if u.opts.dataDir != "" {
		return u.opts.dataDir
	}
	if d := dataDirFromUnit(filepath.Join(u.systemd, "codeplod.service")); d != "" {
		return d
	}
	if d := os.Getenv("CODEPLO_DATA_DIR"); d != "" {
		return d
	}
	return defaultDataDir
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// pendingRevert reports whether the hardening spool holds snapshots that `codeplo revert` could still restore.
func pendingRevert(dataDir string) bool {
	ents, err := os.ReadDir(filepath.Join(dataDir, "revert"))
	if err != nil {
		return false
	}
	for _, e := range ents {
		if e.IsDir() {
			if _, err := os.Stat(filepath.Join(dataDir, "revert", e.Name(), "manifest.json")); err == nil {
				return true
			}
		}
	}
	return false
}

func (u *uninstaller) say(format string, a ...any) { fmt.Fprintf(u.out, format+"\n", a...) }

func (u *uninstaller) step(desc string, fn func() error) {
	if u.opts.dry {
		u.say("  [dry-run] %s", desc)
		return
	}
	if err := fn(); err != nil {
		u.failures = append(u.failures, desc+": "+err.Error())
		u.say("  FAIL  %s: %v", desc, err)
		return
	}
	u.say("  ok    %s", desc)
}

func (u *uninstaller) sh(name string, args ...string) error {
	out, err := u.run(name, args...)
	if err != nil {
		if out != "" {
			return fmt.Errorf("%w (%s)", err, out)
		}
		return err
	}
	return nil
}

func removeAll(paths ...string) error {
	var first error
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (u *uninstaller) userExists(name string) bool {
	_, err := u.run("id", "-u", name)
	return err == nil
}

func (u *uninstaller) execute() error {
	data := u.resolveDataDir()
	if !u.opts.keepData {
		if err := safeDataDir(data); err != nil {
			return err
		}
	}
	opUser, opCreated := "", false
	if m := readTrim(filepath.Join(data, "operator", operatorMarker)); m != "" {
		opUser = m
		opCreated = readTrim(filepath.Join(data, "operator", operatorCreated)) != ""
	}
	removeOperator := opUser != "" && opCreated && !u.opts.keepOperator && !u.opts.keepData
	removeServiceUser := !u.opts.keepData
	pending := pendingRevert(data)
	systemd := u.has("systemctl")

	u.say("codeplo uninstall plan (data dir: %s)", data)
	if u.opts.revertHardening {
		u.say("  - revert pending hardening changes (codeplo revert all)")
	} else if pending {
		u.say("  ! pending hardening snapshots exist and will NOT be reverted (add --revert-hardening)")
	}
	u.say("  - stop and disable codeplod, codeplo-caddy, codeplo-pty.socket; remove their unit files")
	if u.opts.workloads {
		u.say("  - remove containers, networks and volumes labelled %s", managedLabel)
	} else {
		u.say("  = leave workload containers/volumes in place (add --remove-workloads to remove them)")
	}
	u.say("  - remove %s%s", u.etc, map[bool]string{true: " and " + u.envFile}[u.opts.purgeConfig])
	if u.opts.keepData {
		u.say("  = keep %s and the %s/operator users (--keep-data)", data, serviceUser)
	} else {
		u.say("  - delete %s (database, master key, certificates, operator key, update backups)", data)
	}
	if removeOperator {
		u.say("  - delete user %s and its home (created by the installer)", opUser)
	} else if opUser != "" {
		u.say("  = keep user %s (not created by the installer, or kept by flag)", opUser)
	}
	if removeServiceUser {
		u.say("  - delete user %s", serviceUser)
	}
	u.say("  - delete %s/codeplod and %s/codeplo", u.bin, u.bin)
	u.say("  = applied firewall/sshd hardening is not undone")

	if u.opts.dry {
		u.say("dry run: nothing changed")
		return nil
	}
	if !u.opts.yes {
		fmt.Fprintf(u.out, "Type %q to continue: ", confirmPhrase)
		line, _ := bufio.NewReader(u.in).ReadString('\n')
		if strings.TrimSpace(line) != confirmPhrase {
			return fmt.Errorf("aborted; nothing changed")
		}
	}

	if u.opts.revertHardening {
		var rerr error
		u.step("revert pending hardening", func() error {
			rerr = u.sh("runuser", "-u", serviceUser, "--", "env", "CODEPLO_DATA_DIR="+data, u.self, "revert", "all")
			// "no snapshots" is the normal answer on a host that never applied anything.
			if rerr != nil && strings.Contains(rerr.Error(), "no snapshots") {
				return nil
			}
			return rerr
		})
		if rerr != nil && !strings.Contains(rerr.Error(), "no snapshots") && !u.opts.force {
			return fmt.Errorf("hardening revert failed; fix it or re-run with --force (nothing else was changed)")
		}
	}

	if systemd {
		u.step("stop and disable services", func() error {
			// Not fatal if a unit is already gone: disable --now on a missing unit is the clean-host case.
			_ = u.sh("systemctl", "disable", "--now", "codeplod", "codeplo-pty.socket", "codeplo-caddy")
			_ = u.sh("systemctl", "stop", "codeplo-pty@*")
			if out, _ := u.run("systemctl", "is-active", "codeplod"); strings.TrimSpace(out) == "active" {
				return fmt.Errorf("codeplod is still active")
			}
			return nil
		})
	}

	if u.opts.workloads {
		u.removeWorkloads()
	}

	if systemd {
		u.step("remove unit files", func() error {
			return removeAll(
				filepath.Join(u.systemd, "codeplod.service"),
				filepath.Join(u.systemd, "codeplo-caddy.service"),
				filepath.Join(u.systemd, "codeplo-pty.socket"),
				filepath.Join(u.systemd, "codeplo-pty@.service"),
				filepath.Join(u.systemd, "codeplod.service.d"),
			)
		})
		u.step("reload systemd", func() error {
			if err := u.sh("systemctl", "daemon-reload"); err != nil {
				return err
			}
			_ = u.sh("systemctl", "reset-failed")
			return nil
		})
	}

	u.step("remove "+u.etc, func() error { return removeAll(u.etc) })
	if u.opts.purgeConfig {
		u.step("remove "+u.envFile, func() error { return removeAll(u.envFile) })
	}
	if !u.opts.keepData {
		u.step("delete "+data, func() error { return removeAll(data) })
	}

	if removeOperator {
		u.step("delete user "+opUser, func() error { return u.deleteUser(opUser, true) })
	}
	if removeServiceUser {
		u.step("delete user "+serviceUser, func() error { return u.deleteUser(serviceUser, false) })
	}

	// The running binary goes last. Unlinking an executable that is running is fine on Linux.
	u.step("delete binaries", func() error {
		return removeAll(filepath.Join(u.bin, "codeplod"), filepath.Join(u.bin, "codeplo"))
	})

	if len(u.failures) > 0 {
		u.say("")
		u.say("finished with %d problem(s); fix these by hand:", len(u.failures))
		for _, f := range u.failures {
			u.say("  - %s", f)
		}
		return fmt.Errorf("uninstall incomplete")
	}
	u.say("")
	u.say("codeplo is removed. Reinstall with the install script whenever you like.")
	return nil
}

func (u *uninstaller) deleteUser(name string, removeHome bool) error {
	if !u.userExists(name) {
		return nil // already gone
	}
	// A login shell or a leftover process owned by the user makes userdel refuse.
	_, _ = u.run("pkill", "-KILL", "-u", name)
	args := []string{name}
	if removeHome {
		args = []string{"-r", name}
	}
	return u.sh("userdel", args...)
}

func (u *uninstaller) removeWorkloads() {
	if !u.has("docker") {
		u.say("  skip  workloads: docker is not installed")
		return
	}
	for _, kind := range []struct{ list, rm []string }{
		{[]string{"ps", "-aq", "--filter", "label=" + managedLabel}, []string{"rm", "-f"}},
		{[]string{"network", "ls", "-q", "--filter", "label=" + managedLabel}, []string{"network", "rm"}},
		{[]string{"volume", "ls", "-q", "--filter", "label=" + managedLabel}, []string{"volume", "rm", "-f"}},
	} {
		kind := kind
		u.step("remove managed docker "+kind.rm[0], func() error {
			out, err := u.run("docker", kind.list...)
			if err != nil {
				return fmt.Errorf("%w (%s)", err, out)
			}
			ids := strings.Fields(out)
			if len(ids) == 0 {
				return nil
			}
			return u.sh("docker", append(append([]string{}, kind.rm...), ids...)...)
		})
	}
}
