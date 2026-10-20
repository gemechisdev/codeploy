// drivers.go holds the concrete, distro-correct bodies the planner emits: one Driver per applicable check with
// idempotent argv, real file contents and a read-back that proves the end state. Nothing here runs a shell:
// every command is a Cmd executed through platform.Runner.

package hardening

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/codeplo/codeplo/internal/platform"
)

// Paths a driver owns on the host. Absolute paths are interpreted under Env.Root when apply runs.
const (
	AutoUpgradesPath = "/etc/apt/apt.conf.d/20auto-upgrades"
	SysctlConfPath   = "/etc/sysctl.d/60-codeplo.conf"
	Fail2banJailPath = "/etc/fail2ban/jail.d/codeplo.local"

	// NftTableName is the only netfilter object codeplo creates, edits or removes. filter, nat and every
	// Docker-provisioned chain are never referenced by any nft command below.
	NftTableName = "codeplo"
	// NftRulesetPath is the apply-time ruleset. It lives under codeplo's own state directory, so the snapshot
	// covers it and revert removes it; argv carries it as one argument.
	NftRulesetPath = "/var/lib/codeplo/plan/codeplo.nft"
	// NftBootTablePath and NftBootConfPath belong to the persist step, which runs at confirm only.
	NftBootTablePath = "/etc/nftables/codeplo.nft"
	NftBootConfPath  = "/etc/nftables.conf"
	// NftIncludeLine is the single line codeplo adds to the distro's boot conf; the rest of that file stays
	// exactly as the operator wrote it.
	NftIncludeLine = "include " + NftBootTablePath
)

// Driver is one check's complete change set plus the verification that proves the end state, so re-applying a
// satisfied check is a no-op.
type Driver struct {
	Key   string
	Pre   []Cmd
	Files []FileChange
	Post  []Cmd
	// Verify reads the effective state back (files through Env, live values through run) and reports whether the
	// check is satisfied now. A nil Verify means the detector in audit.go already reads the same state.
	Verify func(ctx context.Context, env Env, run platform.Runner) (bool, string)
}

// aptInstall never prompts: --force-confold keeps the host's own conffile edits, --force-confdef takes the
// maintainer default where there is none. Installing an already-installed package is a no-op.
func aptInstall(packages ...string) Cmd {
	args := []string{"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "install", "-y"}
	return Cmd{"apt-get", append(args, packages...)}
}

// readAbs reads a host-absolute path through Env so verification honours a test root.
func readAbs(e Env, path string) (string, bool) { return e.read(strings.TrimPrefix(path, "/")) }

const autoUpgradesBody = "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n"

// UnattendedUpgrades is D6 on apt distributions: the package supplies the periodic config and the timers,
// 20auto-upgrades turns the nightly security upgrade on.
func UnattendedUpgrades() Driver {
	return Driver{
		Key:   "upd.unattended",
		Pre:   []Cmd{aptInstall("unattended-upgrades")},
		Files: []FileChange{{Path: AutoUpgradesPath, Content: autoUpgradesBody, Mode: 0o644}},
		Post:  []Cmd{{"systemctl", []string{"enable", "--now", "apt-daily-upgrade.timer"}}},
		Verify: func(ctx context.Context, env Env, run platform.Runner) (bool, string) {
			s, ok := readAbs(env, AutoUpgradesPath)
			if !ok {
				return false, AutoUpgradesPath + " is not present"
			}
			if !strings.Contains(s, `APT::Periodic::Unattended-Upgrade "1";`) || !strings.Contains(s, `APT::Periodic::Update-Package-Lists "1";`) {
				return false, "20auto-upgrades does not enable the nightly security upgrade"
			}
			if run == nil {
				return true, "20auto-upgrades enabled (timer not checked: no command runner)"
			}
			if !platform.SystemdActive(ctx, run, "apt-daily-upgrade.timer") {
				return false, "20auto-upgrades enabled but apt-daily-upgrade.timer is not active"
			}
			return true, "unattended-upgrades enabled and apt-daily-upgrade.timer active"
		},
	}
}

// sysctlBaseline declares the kernel settings once: the file body and the read-back both come from it, so the
// two can never drift apart.
var sysctlBaseline = [][2]string{
	{"net.ipv4.conf.all.rp_filter", "1"},
	{"net.ipv4.conf.default.rp_filter", "1"},
	{"net.ipv4.tcp_syncookies", "1"},
	{"net.ipv4.conf.all.accept_redirects", "0"},
	{"net.ipv4.conf.default.accept_redirects", "0"},
	{"net.ipv6.conf.all.accept_redirects", "0"},
	{"net.ipv4.conf.all.send_redirects", "0"},
	{"net.ipv4.icmp_echo_ignore_broadcasts", "1"},
}

func sysctlBody() string {
	var b strings.Builder
	b.WriteString("# managed by codeplo\n")
	for _, kv := range sysctlBaseline {
		b.WriteString(kv[0] + " = " + kv[1] + "\n")
	}
	return b.String()
}

// sysctlProc is the /proc file holding a setting's live value (net.ipv4.x -> proc/sys/net/ipv4/x).
func sysctlProc(key string) string { return "proc/sys/" + strings.ReplaceAll(key, ".", "/") }

// Sysctl is D8: one managed drop-in plus the load. Writing the file is what survives a reboot; sysctl -p makes
// it live now, and re-applying the same values is harmless.
func Sysctl() Driver {
	return Driver{
		Key:   "kern.sysctl",
		Files: []FileChange{{Path: SysctlConfPath, Content: sysctlBody(), Mode: 0o644}},
		Post:  []Cmd{{"sysctl", []string{"-p", SysctlConfPath}}},
		Verify: func(_ context.Context, env Env, _ platform.Runner) (bool, string) {
			var unreadable, wrong []string
			for _, kv := range sysctlBaseline {
				got, ok := env.read(sysctlProc(kv[0]))
				if !ok {
					unreadable = append(unreadable, kv[0])
					continue
				}
				if strings.TrimSpace(got) != kv[1] {
					wrong = append(wrong, kv[0]+"="+strings.TrimSpace(got))
				}
			}
			switch {
			case len(unreadable) > 0:
				return false, "live value not readable for " + strings.Join(unreadable, ", ")
			case len(wrong) > 0:
				return false, "not applied: " + strings.Join(wrong, ", ")
			}
			return true, "all " + strconv.Itoa(len(sysctlBaseline)) + " baseline sysctls are live"
		},
	}
}

// Fail2banJailBody renders the managed jail. ignoreip is written first and always carries loopback plus every
// current admin/operator IP: a jail without them can lock the operator out of the host.
func Fail2banJailBody(admin []string) string {
	return "# managed by codeplo\n[DEFAULT]\nignoreip = " + strings.Join(IgnoreList(admin), " ") + "\n\n[sshd]\nenabled = true\n"
}

// Fail2banJail is the jail as a plan file.
func Fail2banJail(admin []string) FileChange {
	return FileChange{Path: Fail2banJailPath, Content: Fail2banJailBody(admin), Mode: 0o644}
}

// Fail2banIgnoreIPs extracts the ignoreip entries from a jail body (nil when the option is absent).
func Fail2banIgnoreIPs(body string) []string {
	for _, l := range strings.Split(body, "\n") {
		if f := strings.Fields(strings.TrimSpace(l)); len(f) >= 2 && strings.EqualFold(f[0], "ignoreip") {
			v := f[1:]
			// fail2ban's own syntax is `ignoreip = a b`: the assignment sign is a separator, not an address.
			if v[0] == "=" {
				v = v[1:]
			}
			return v
		}
	}
	return nil
}

// Fail2banMissingIPs reports the addresses the ignore list requires but the given jail body does not ignore.
// Empty means the panel user cannot be banned by this jail.
func Fail2banMissingIPs(admin []string, body string) []string {
	have := map[string]bool{}
	for _, ip := range Fail2banIgnoreIPs(body) {
		have[ip] = true
	}
	var miss []string
	for _, ip := range IgnoreList(admin) {
		if !have[ip] {
			miss = append(miss, ip)
		}
	}
	return miss
}

// Fail2ban is D5: install, write the jail with the admin ignore list, enable, then reload (reload keeps the
// running bans and the operator's own session).
func Fail2ban(admin []string) Driver {
	return Driver{
		Key:   "bf.protection",
		Pre:   []Cmd{aptInstall("fail2ban")},
		Files: []FileChange{Fail2banJail(admin)},
		Post:  []Cmd{{"systemctl", []string{"enable", "--now", "fail2ban"}}, {"systemctl", []string{"reload", "fail2ban"}}},
		Verify: func(ctx context.Context, env Env, run platform.Runner) (bool, string) {
			s, ok := readAbs(env, Fail2banJailPath)
			if !ok {
				return false, Fail2banJailPath + " is not present"
			}
			// Daemon state is reported before the jail contents: the check claims protection is RUNNING, and a
			// stopped fail2ban bans nobody, so a jail problem on disk is not yet the reason to act.
			if run != nil && !platform.SystemdActive(ctx, run, "fail2ban") {
				return false, "fail2ban is not active; the jail on disk is not in force"
			}
			if miss := Fail2banMissingIPs(admin, s); len(miss) > 0 {
				return false, "jail would be able to ban its own operator: " + strings.Join(miss, ", ")
			}
			if run == nil {
				return true, "ignore list complete (daemon state not checked: no command runner)"
			}
			return true, "fail2ban active; ignoreip: " + strings.Join(Fail2banIgnoreIPs(s), " ")
		},
	}
}

// NftAllowedPorts returns the tcp ports the dedicated table must accept: every sshd listener plus the edge's
// 80/443, de-duplicated and sorted (nft rejects a set literal with duplicate elements).
func NftAllowedPorts(sshPorts []int) []int {
	set := map[int]bool{80: true, 443: true}
	for _, p := range sshPorts {
		if p > 0 && p < 65536 {
			set[p] = true
		}
	}
	var out []int
	for p := range set {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// NftRuntimeRuleset renders the apply-time file. destroy (nft >= 0.9.7) is the idempotent delete of our own
// table: it succeeds when the table is absent, so applying the same ruleset twice leaves one identical table
// instead of appended duplicate rules. Nothing outside table inet codeplo is destroyed.
func NftRuntimeRuleset(ports []int) string {
	return "#!/usr/sbin/nft -f\n# managed by codeplo: dedicated table only. Nothing outside table inet " + NftTableName +
		" is created, edited or removed, and the existing ruleset is never flushed.\ndestroy table inet " + NftTableName + "\n" + NftRuleset(ports)
}

// NftBootRuleset renders the persisted table (confirm only). No destroy: at boot nothing of ours exists yet.
func NftBootRuleset(ports []int) string {
	return "#!/usr/sbin/nft -f\n# managed by codeplo (persisted when a firewall change is confirmed)\n" + NftRuleset(ports)
}

// NftRulesetIsAdditive reports whether a ruleset body only ever creates codeplo's own table: any flush
// instruction, and any delete or destroy of an object that is not table inet codeplo, is refused. Comment
// lines carry no verdict, so the wording inside the managed header cannot trip this check.
func NftRulesetIsAdditive(body string) bool {
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(strings.ToLower(l))
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.Contains(t, "flush") {
			return false
		}
		f := strings.Fields(t)
		if len(f) < 2 || (f[0] != "delete" && f[0] != "destroy") {
			continue
		}
		ownTable := f[1] == "table" && len(f) == 4 && f[3] == NftTableName
		if !ownTable {
			return false
		}
	}
	return true
}

// NftApplyIsRuntimeOnly proves the apply step loads runtime state only: writing the boot conf or enabling the
// nftables service happens at confirm, never at apply.
func NftApplyIsRuntimeOnly(cmds []Cmd) bool {
	for _, c := range cmds {
		s := strings.ToLower(c.String())
		if strings.Contains(s, strings.ToLower(NftBootConfPath)) || strings.Contains(s, strings.ToLower(NftBootTablePath)) ||
			strings.Contains(s, "enable nftables") {
			return false
		}
	}
	return true
}

var nftPortSetRe = regexp.MustCompile(`(?im)tcp\s+dport\s+\{([^}]*)\}`)

// nftLiveTCPPorts reads the accepted tcp port set back out of `nft list table inet codeplo` output.
func nftLiveTCPPorts(out string) ([]int, bool) {
	m := nftPortSetRe.FindStringSubmatch(out)
	if m == nil {
		return nil, false
	}
	var got []int
	for _, s := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }) {
		if n, err := strconv.Atoi(s); err == nil {
			got = append(got, n)
		}
	}
	sort.Ints(got)
	return got, len(got) > 0
}

// Nftables is the runtime-first half of D2 (P2b): write the ruleset where codeplo owns the directory, load it
// with nft -f, read the live table back. Persistence lives in NftablesBootFiles/NftablesBootCmds.
func Nftables(ports []int) Driver {
	ports = NftAllowedPorts(ports)
	return Driver{
		Key:   "fw.default_deny",
		Files: []FileChange{{Path: NftRulesetPath, Content: NftRuntimeRuleset(ports), Mode: 0o644}},
		Post:  []Cmd{{"nft", []string{"-f", NftRulesetPath}}},
		Verify: func(ctx context.Context, _ Env, run platform.Runner) (bool, string) {
			if run == nil {
				return false, "no command runner: the live ruleset cannot be read back"
			}
			out, err := run(ctx, "nft", "list", "table", "inet", NftTableName)
			if err != nil {
				return false, "table inet " + NftTableName + " is not loaded: " + strings.TrimSpace(out)
			}
			if !strings.Contains(out, "policy drop") {
				return false, "table inet " + NftTableName + " has no default-deny policy"
			}
			got, ok := nftLiveTCPPorts(out)
			if !ok {
				return false, "no tcp dport set in the live table: " + strings.TrimSpace(out)
			}
			if !samePorts(got, ports) {
				return false, "live tcp ports " + portList(got) + " differ from the plan " + portList(ports)
			}
			return true, "table inet " + NftTableName + " live with policy drop; tcp " + portList(got)
		},
	}
}

func portList(ports []int) string {
	ps := make([]string, 0, len(ports))
	for _, p := range ports {
		ps = append(ps, strconv.Itoa(p))
	}
	return "{" + strings.Join(ps, " ") + "}"
}

func samePorts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NftablesBootFiles renders the persist-to-boot state: the table in a file codeplo owns, plus ONE include line
// appended to the distro's boot conf, whose existing content is preserved verbatim. A conf that already carries
// the include yields no second file, so confirming twice writes nothing more.
func NftablesBootFiles(ports []int, existingBootConf string) []FileChange {
	out := []FileChange{{Path: NftBootTablePath, Content: NftBootRuleset(NftAllowedPorts(ports)), Mode: 0o644}}
	if strings.Contains(existingBootConf, NftIncludeLine) {
		return out
	}
	conf := existingBootConf
	if conf == "" {
		conf = "#!/usr/sbin/nft -f\n"
	}
	if !strings.HasSuffix(conf, "\n") {
		conf += "\n"
	}
	conf += NftIncludeLine + "\n"
	return append(out, FileChange{Path: NftBootConfPath, Content: conf, Mode: 0o644})
}

// NftablesBootCmds arms the unit that loads /etc/nftables.conf at boot. enable (not --now) leaves the running
// ruleset exactly as the runtime apply left it.
func NftablesBootCmds() []Cmd {
	return []Cmd{{"systemctl", []string{"enable", "nftables"}}}
}

// sshdUnitFor is the distro-correct systemd unit owning sshd: ssh on Debian/Ubuntu, sshd on everything else.
func sshdUnitFor(distro string) string {
	if distro == "" || platform.SupportedDistro(distro) {
		return "ssh"
	}
	return "sshd"
}

// sshExpect names the effective sshd settings that must satisfy each Tier 3 check.
var sshExpect = map[string][]string{
	"ssh.no_password":      {"passwordauthentication"},
	"ssh.no_root_password": {"permitrootlogin"},
	"ssh.limit_auth":       {"permitemptypasswords", "maxauthtries"},
}

// SSHEffective parses `sshd -T` output (one "setting value" pair per line, keys lower-cased).
func SSHEffective(dump string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(dump, "\n") {
		if f := strings.Fields(strings.TrimSpace(l)); len(f) >= 2 {
			out[strings.ToLower(f[0])] = strings.ToLower(strings.Join(f[1:], " "))
		}
	}
	return out
}

func sshValueAllowed(got string, allowed []string) bool {
	for _, a := range allowed {
		if got == a {
			return true
		}
	}
	return false
}

// sshExpectedValues mirrors the drop-in bodies: what a check demands of the effective configuration.
var sshExpectedValues = map[string][]string{
	"passwordauthentication": {"no"},
	"permitrootlogin":        {"no", "prohibit-password", "without-password"},
	"permitemptypasswords":   {"no"},
}

// SSHValuesSatisfied compares the EFFECTIVE values sshd reports (after drop-in ordering) with what the check
// requires. This is the verification that closes P2c: the drop-in is only done when sshd -T agrees.
func SSHValuesSatisfied(key string, eff map[string]string) (bool, string) {
	settings, known := sshExpect[key]
	if !known {
		return false, "no effective-value expectation for " + key
	}
	var seen []string
	for _, s := range settings {
		v, ok := eff[s]
		if !ok {
			return false, s + " missing from sshd -T"
		}
		seen = append(seen, s+"="+v)
		if s == "maxauthtries" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 4 {
				return false, "maxauthtries " + v + " is outside 1..4"
			}
			continue
		}
		if want, ok := sshExpectedValues[s]; !ok || !sshValueAllowed(v, want) {
			return false, s + " is " + v
		}
	}
	return true, "effective: " + strings.Join(seen, " ")
}

// SSHDropin is the guarded Tier 3 change set (D3): write only the managed drop-in — the main sshd_config is
// never edited — validate with sshd -t, then RELOAD. A restart would drop the operator's own sessions.
func SSHDropin(distro, key string) Driver {
	unit := sshdUnitFor(distro)
	return Driver{
		Key:   key,
		Files: []FileChange{{Path: sshDropin, Content: sshBody(key), Mode: 0o600}},
		Post:  []Cmd{{"sshd", []string{"-t"}}, {"systemctl", []string{"reload", unit}}},
		Verify: func(ctx context.Context, _ Env, run platform.Runner) (bool, string) {
			if run == nil {
				return false, "no command runner: sshd -T cannot be read"
			}
			out, err := run(ctx, "sshd", "-T")
			if err != nil {
				return false, "sshd -T failed: " + strings.TrimSpace(out)
			}
			return SSHValuesSatisfied(key, SSHEffective(out))
		},
	}
}

// sshReloadOnly proves no command restarts or stops sshd, which is what keeps existing sessions alive.
func sshReloadOnly(cmds []Cmd) bool {
	for _, c := range cmds {
		s := strings.ToLower(c.String())
		if strings.Contains(s, "restart") || strings.Contains(s, "stop") {
			return false
		}
	}
	return true
}

// ErrKeyLoginUnproven is returned when a Tier 3 SSH apply is attempted without proof that key login works.
// Without that proof a rejected drop-in means permanent lockout, so there is no override path.
var ErrKeyLoginUnproven = errors.New("ssh apply refused: key login is not proven")

// SSHBlockingPreflight lists the Tier 3 items that must pass before an SSH apply. The console-access
// acknowledgement travels in the apply request and is audited by the caller; everything else is decided here.
var SSHBlockingPreflight = []string{"include", "sshd_t", "authorized_keys", "pubkey_evidence"}

// SSHApplyGuard refuses a Tier 3 apply unless every blocking preflight item passed.
func SSHApplyGuard(items []PreflightItem) error {
	passed := map[string]bool{}
	for _, i := range items {
		passed[i.Key] = i.OK
	}
	var blocked []string
	for _, k := range SSHBlockingPreflight {
		if !passed[k] {
			blocked = append(blocked, k)
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("%w: blocking items not satisfied: %s", ErrKeyLoginUnproven, strings.Join(blocked, ", "))
	}
	return nil
}
