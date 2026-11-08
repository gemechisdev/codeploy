package hardening

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/codeplo/codeplo/internal/platform"
)

// BackupRoot is where a hand-run plan keeps the copies of the files it replaces and its undo script.
const BackupRoot = "/var/backups/codeplo"

// manualRevertDelay is how long a hand-run Tier 2/3 plan waits for you to confirm before it undoes itself.
const manualRevertDelay = "300s"

var safeArg = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shQuote renders one argument for a POSIX shell: left bare when it is plainly safe, single-quoted otherwise.
func shQuote(s string) string {
	if s != "" && safeArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (c Cmd) shell() string {
	parts := []string{shQuote(c.Name)}
	for _, a := range c.Args {
		parts = append(parts, shQuote(a))
	}
	// A command that reads stdin would swallow the rest of a script that is itself fed through stdin.
	return strings.Join(parts, " ") + " </dev/null"
}

// uniqueDelim returns a here-document delimiter that never appears as a whole line of any body.
func uniqueDelim(base string, bodies ...string) string {
	d := base
	for {
		clash := false
		for _, b := range bodies {
			for _, ln := range strings.Split(b, "\n") {
				if ln == d {
					clash = true
				}
			}
		}
		if !clash {
			return d
		}
		d += "_X"
	}
}

func ensureNL(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}
	return s + "\n"
}

// runtimeUndo is the live-state step that goes with a check's files when it is undone.
func runtimeUndo(key, distro string) []string {
	switch key {
	case "fw.default_deny":
		return []string{"nft delete table inet " + NftTableName}
	case "ssh.no_password", "ssh.no_root_password", "ssh.limit_auth":
		return []string{"systemctl reload " + sshdUnitFor(distro)}
	case "bf.protection":
		return []string{"systemctl reload fail2ban"}
	}
	return nil
}

// ScriptBody renders the plan as the shell program a root user runs. It is deterministic and self-contained:
// every file body is embedded, every command is real argv, and nothing relies on the codeplo daemon.
//
//  1. back up each file it will replace under $B and write $B/revert.sh (the undo)
//  2. Tier 2/3 only: arm a timer that runs the undo if you do not confirm in time
//  3. pre commands, then each file (temp file + chmod + atomic rename), then post commands
//  4. any failure restores the backups before the shell exits
func (p Plan) ScriptBody(distro, backupRoot string, armTimer bool) string {
	if len(p.Pre) == 0 && len(p.Files) == 0 && len(p.Post) == 0 {
		return ""
	}
	var bodies []string
	for _, f := range p.Files {
		bodies = append(bodies, f.Content)
	}
	revDelim := uniqueDelim("CODEPLO_REVERT", bodies...)
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("# codeplo: %s (tier %d). Run as root. Backups and an undo script go to $B.", p.CheckKey, p.Tier)
	w("set -euo pipefail")
	w("B=%s/$(date +%%Y%%m%%dT%%H%%M%%S)-%s", backupRoot, strings.ReplaceAll(p.CheckKey, ".", "-"))
	w(`install -d -m 0700 "$B"`)
	for _, f := range p.Files {
		w(`if [ -e %s ]; then cp -a --parents %s "$B"; fi`, shQuote(f.Path), shQuote(f.Path))
	}
	w("cat > \"$B/revert.sh\" <<'%s'", revDelim)
	w("# Undo for %s: restores the saved files and the live state.", p.CheckKey)
	w(`B="$(dirname "$0")"`)
	w("set +e")
	for _, f := range p.Files {
		w(`if [ -e "$B"%s ]; then cp -a "$B"%s %s; else rm -f %s; fi`, shQuote(f.Path), shQuote(f.Path), shQuote(f.Path), shQuote(f.Path))
	}
	for _, c := range runtimeUndo(p.CheckKey, distro) {
		w("%s", c+" </dev/null")
	}
	w("%s", revDelim)
	w(`trap 'echo "step failed: undoing" >&2; sh "$B/revert.sh"' ERR`)

	if armTimer && p.Tier >= 2 {
		w("# Dead-man timer: if you do not cancel it, the undo runs by itself in %s.", manualRevertDelay)
		w(`U=codeplo-manual-revert-$(basename "$B")`)
		w(`systemd-run --quiet --unit="$U" --on-active=%s /bin/sh "$B/revert.sh"`, manualRevertDelay)
	}

	for _, c := range p.Pre {
		w("%s", c.shell())
	}
	for i, f := range p.Files {
		delim := uniqueDelim(fmt.Sprintf("CODEPLO_FILE_%d", i+1), f.Content)
		tmp := f.Path + ".codeplo-tmp"
		w("install -d -m 0755 %s", shQuote(filepath.Dir(f.Path)))
		w("cat > %s <<'%s'", shQuote(tmp), delim)
		b.WriteString(ensureNL(f.Content))
		w("%s", delim)
		w("chmod %04o %s", uint32(f.Mode.Perm()), shQuote(tmp))
		w("mv -f %s %s", shQuote(tmp), shQuote(f.Path))
	}
	for _, c := range p.Post {
		w("%s", c.shell())
	}
	w("trap - ERR")
	w(`echo "codeplo: %s applied. Undo any time with: sudo sh $B/revert.sh"`, p.CheckKey)
	if armTimer && p.Tier >= 2 {
		w(`echo "Open a NEW terminal/SSH session now to confirm you still have access."`)
		w(`echo "Confirmed? Cancel the automatic undo: sudo systemctl stop $U.timer"`)
		w(`echo "Otherwise this change undoes itself in %s."`, manualRevertDelay)
	}
	return b.String()
}

// Script is the single command shown to the operator: one paste into a terminal on the server.
func (p Plan) Script() string {
	body := p.ScriptBody(platform.DistroID(), BackupRoot, true)
	if body == "" {
		return ""
	}
	outer := uniqueDelim("CODEPLO_APPLY", body)
	return "sudo bash <<'" + outer + "'\n" + ensureNL(body) + outer
}
