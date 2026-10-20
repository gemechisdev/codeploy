package hardening

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codeplo/codeplo/internal/platform"
)

// liveNftTable is what `nft list table inet codeplo` prints after a successful apply.
const liveNftTable = "table inet codeplo {\n\tchain input {\n\t\ttype filter hook input priority 0; policy drop;\n" +
	"\t\tct state established,related accept\n\t\tiif \"lo\" accept\n\t\tip protocol icmp accept\n" +
	"\t\tip6 nexthdr ipv6-icmp accept\n\t\ttcp dport { 22, 80, 443 } accept\n\t}\n}\n"

// liveRuleset is a real host's `nft list ruleset`: the Docker-provisioned chains exist and must survive.
const liveRuleset = "table ip nat {\n\tchain docker {\n\t}\n}\n" +
	"table ip filter {\n\tchain DOCKER-USER {\n\t}\n}\n" + liveNftTable

// fakeRunner records argv and answers by command name. It executes nothing: this suite runs where apt, systemd
// and nft do not exist.
type fakeRunner struct {
	calls []Cmd
	out   map[string]string
	err   map[string]error
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, error) {
	f.calls = append(f.calls, Cmd{Name: name, Args: args})
	return f.out[name], f.err[name]
}

// wantCmd asserts one recorded invocation matches argv exactly: the name plus every separate argument, in order.
func wantCmd(t *testing.T, got Cmd, name string, args ...string) {
	t.Helper()
	if got.Name != name || fmt.Sprint(got.Args) != fmt.Sprint(args) {
		t.Fatalf("argv = %s %q, want %s %q", got.Name, got.Args, name, args)
	}
}

func writeFixture(t *testing.T, root, absPath, content string) {
	t.Helper()
	f := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(absPath, "/")))
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFixture(t *testing.T, root, absPath string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(absPath, "/"))))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func sameStrings(a, b []string) bool {
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

func byKey(items []PreflightItem, key string) string {
	for _, i := range items {
		if i.Key == key {
			return i.Detail
		}
	}
	return ""
}

func byKeyOK(items []PreflightItem, key string) bool {
	for _, i := range items {
		if i.Key == key {
			return i.OK
		}
	}
	return false
}

// A driver that shelled out could run text an attacker controls, so argv-only is an invariant, not a style rule.
func TestDriversNeverRunAShell(t *testing.T) {
	drivers := []Driver{UnattendedUpgrades(), Sysctl(), Fail2ban([]string{"203.0.113.9"}), Nftables([]int{22}),
		SSHDropin("ubuntu", "ssh.no_password"), SSHDropin("fedora", "ssh.limit_auth")}
	for _, d := range drivers {
		for _, c := range append(append([]Cmd{}, d.Pre...), d.Post...) {
			if c.Name == "sh" || c.Name == "bash" {
				t.Fatalf("%s: shell command %q", d.Key, c.Name)
			}
			for _, a := range c.Args {
				if a == "-c" || a == "-lc" || a == "-i" {
					t.Fatalf("%s: %s takes the shell flag %q", d.Key, c.Name, a)
				}
			}
		}
	}
}

func TestUnattendedUpgradesDriver(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	d := UnattendedUpgrades()
	wantCmd(t, d.Pre[0], "apt-get", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "unattended-upgrades")
	wantCmd(t, d.Post[0], "systemctl", "enable", "--now", "apt-daily-upgrade.timer")
	if len(d.Files) != 1 || d.Files[0].Path != AutoUpgradesPath {
		t.Fatalf("files = %+v", d.Files)
	}
	for _, want := range []string{`APT::Periodic::Update-Package-Lists "1";`, `APT::Periodic::Unattended-Upgrade "1";`} {
		if !strings.Contains(d.Files[0].Content, want) {
			t.Fatalf("body missing %q", want)
		}
	}
	if ok, _ := d.Verify(ctx, Env{Root: root}, (&fakeRunner{}).run); ok {
		t.Fatal("verify passed with no config on disk")
	}
	writeFixture(t, root, AutoUpgradesPath, d.Files[0].Content)
	active := &fakeRunner{out: map[string]string{"systemctl": "active"}}
	ok, detail := d.Verify(ctx, Env{Root: root}, active.run)
	if !ok {
		t.Fatalf("verify: %s", detail)
	}
	inactive := &fakeRunner{out: map[string]string{"systemctl": "inactive"}}
	if ok, detail = d.Verify(ctx, Env{Root: root}, inactive.run); ok || !strings.Contains(detail, "apt-daily-upgrade.timer") {
		t.Fatalf("inactive timer accepted: %s", detail)
	}
}

func TestSysctlBodyAndLiveReadBack(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	d := Sysctl()
	if len(d.Files) != 1 || d.Files[0].Path != SysctlConfPath {
		t.Fatalf("files = %+v", d.Files)
	}
	body := d.Files[0].Content
	for _, kv := range sysctlBaseline {
		if !strings.Contains(body, kv[0]+" = "+kv[1]+"\n") {
			t.Fatalf("body missing %q:\n%s", kv[0]+" = "+kv[1], body)
		}
	}
	wantCmd(t, d.Post[0], "sysctl", "-p", SysctlConfPath)
	for _, kv := range sysctlBaseline {
		writeFixture(t, root, "/"+sysctlProc(kv[0]), kv[1]+"\n")
	}
	ok, detail := d.Verify(ctx, Env{Root: root}, nil)
	if !ok {
		t.Fatalf("verify: %s", detail)
	}
	// the verification reads live kernel values, so one wrong value names the key that differs
	writeFixture(t, root, "/"+sysctlProc("net.ipv4.tcp_syncookies"), "0\n")
	if ok, detail = d.Verify(ctx, Env{Root: root}, nil); ok || !strings.Contains(detail, "net.ipv4.tcp_syncookies=0") {
		t.Fatalf("wrong live value accepted: %s", detail)
	}
	if ok, detail = d.Verify(ctx, Env{Root: t.TempDir()}, nil); ok || !strings.Contains(detail, "not readable") {
		t.Fatalf("unreadable host accepted: %s", detail)
	}
}

func TestFail2banJailAlwaysIgnoresTheOperator(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	admin := []string{"203.0.113.9", "203.0.113.9", "198.51.100.7", "not-an-ip", "127.0.0.1"}
	body := Fail2banJailBody(admin)
	want := []string{"127.0.0.1/8", "::1", "203.0.113.9", "198.51.100.7"}
	if got := Fail2banIgnoreIPs(body); !sameStrings(got, want) {
		t.Fatalf("ignoreip = %v, want %v", got, want)
	}
	if miss := Fail2banMissingIPs(admin, body); len(miss) != 0 {
		t.Fatalf("jail does not ignore %v", miss)
	}
	d := Fail2ban(admin)
	wantCmd(t, d.Pre[0], "apt-get", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "fail2ban")
	wantCmd(t, d.Post[0], "systemctl", "enable", "--now", "fail2ban")
	wantCmd(t, d.Post[1], "systemctl", "reload", "fail2ban")
	if d.Files[0].Path != Fail2banJailPath {
		t.Fatalf("jail path %s", d.Files[0].Path)
	}
	active := &fakeRunner{out: map[string]string{"systemctl": "active"}}
	if ok, _ := d.Verify(ctx, Env{Root: root}, active.run); ok {
		t.Fatal("verify passed with no jail on disk")
	}
	writeFixture(t, root, Fail2banJailPath, body)
	if ok, detail := d.Verify(ctx, Env{Root: root}, active.run); !ok {
		t.Fatalf("verify: %s", detail)
	}
	// a jail that would ban the operator is refused even while the daemon is running
	writeFixture(t, root, Fail2banJailPath, "[DEFAULT]\nignoreip = 127.0.0.1/8\n\n[sshd]\nenabled = true\n")
	ok, detail := d.Verify(ctx, Env{Root: root}, active.run)
	if ok || !strings.Contains(detail, "203.0.113.9") {
		t.Fatalf("operator-bannable jail accepted: %s", detail)
	}
	inactive := &fakeRunner{out: map[string]string{"systemctl": "inactive"}}
	if ok, detail = d.Verify(ctx, Env{Root: root}, inactive.run); ok || !strings.Contains(detail, "not active") {
		t.Fatalf("inactive daemon accepted: %s", detail)
	}
}

func TestNftAllowedPortsDedupesAndSorts(t *testing.T) {
	if got := NftAllowedPorts([]int{443, 22, 22, 80, 0, 70000}); !samePorts(got, []int{22, 80, 443}) {
		t.Fatalf("ports = %v", got)
	}
	if got := NftAllowedPorts(nil); !samePorts(got, []int{80, 443}) {
		t.Fatalf("ports = %v", got)
	}
}

func TestNftRuntimeRulesetOnlyOwnsItsTable(t *testing.T) {
	rs := NftRuntimeRuleset(NftAllowedPorts([]int{2222}))
	for _, want := range []string{"table inet codeplo {", "policy drop", "tcp dport { 80, 443, 2222 }",
		"ip6 nexthdr ipv6-icmp", "destroy table inet codeplo"} {
		if !strings.Contains(rs, want) {
			t.Fatalf("ruleset missing %q:\n%s", want, rs)
		}
	}
	if strings.Contains(rs, "docker") || strings.Contains(rs, "nat") {
		t.Fatalf("ruleset references foreign tables:\n%s", rs)
	}
	if !NftRulesetIsAdditive(rs) {
		t.Fatal("runtime ruleset rejected by the additive check")
	}
	if NftBootRuleset([]int{22}) == rs {
		t.Fatal("persist body must not carry the apply-time destroy")
	}
	for _, bad := range []string{"flush ruleset\n", "delete table ip nat\n", "delete chain inet filter input\n",
		"table inet codeplo {\n\tdestroy table inet filter\n}\n"} {
		if NftRulesetIsAdditive(bad) {
			t.Fatalf("additive check accepted %q", bad)
		}
	}
}

func TestNftablesDriverIsRuntimeOnly(t *testing.T) {
	ctx := context.Background()
	d := Nftables([]int{22})
	if len(d.Pre) != 0 {
		t.Fatalf("firewall apply must not install packages: %v", d.Pre)
	}
	if len(d.Files) != 1 || d.Files[0].Path != NftRulesetPath {
		t.Fatalf("files = %+v", d.Files)
	}
	wantCmd(t, d.Post[0], "nft", "-f", NftRulesetPath)
	if !NftApplyIsRuntimeOnly(d.Post) {
		t.Fatalf("apply persists: %v", d.Post)
	}
	if NftApplyIsRuntimeOnly(NftablesBootCmds()) {
		t.Fatal("confirm commands are not the apply set")
	}
	loaded := &fakeRunner{out: map[string]string{"nft": liveNftTable}}
	ok, detail := d.Verify(ctx, Env{}, loaded.run)
	if !ok {
		t.Fatalf("verify: %s", detail)
	}
	if len(loaded.calls) != 1 {
		t.Fatalf("want exactly one live read-back, got %v", loaded.calls)
	}
	wantCmd(t, loaded.calls[0], "nft", "list", "table", "inet", "codeplo")
	if ok, _ := d.Verify(ctx, Env{}, nil); ok {
		t.Fatal("verify passed with no command runner")
	}
	// live ports differing from the plan is drift, not a pass
	wrong := &fakeRunner{out: map[string]string{"nft": strings.Replace(liveNftTable, "22, 80, 443", "80, 443", 1)}}
	if ok, detail = d.Verify(ctx, Env{}, wrong.run); ok || !strings.Contains(detail, "differ") {
		t.Fatalf("drift accepted: %s", detail)
	}
	missing := &fakeRunner{err: map[string]error{"nft": errors.New("No such file or directory")}}
	if ok, detail = d.Verify(ctx, Env{}, missing.run); ok || !strings.Contains(detail, "not loaded") {
		t.Fatalf("absent table accepted: %s", detail)
	}
}

func TestNftablesBootPersistKeepsForeignRules(t *testing.T) {
	existing := "#!/usr/sbin/nft -f\n\nflush ruleset\n\ntable inet filter {\n\tchain input {\n\t\ttype filter hook input priority 0; policy drop;\n\t}\n}\n"
	files := NftablesBootFiles([]int{22}, existing)
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	table, conf := files[0], files[1]
	if table.Path != NftBootTablePath || !NftRulesetIsAdditive(table.Content) {
		t.Fatalf("boot table body wrong: %+v", table)
	}
	if conf.Path != NftBootConfPath {
		t.Fatalf("second file is %s", conf.Path)
	}
	if !strings.HasPrefix(conf.Content, existing) {
		t.Fatalf("existing boot conf rewritten:\n%s", conf.Content)
	}
	if strings.Count(conf.Content, NftIncludeLine) != 1 {
		t.Fatalf("include line count in:\n%s", conf.Content)
	}
	// already included: confirming twice writes no boot conf at all
	again := NftablesBootFiles([]int{22}, conf.Content)
	if len(again) != 1 || again[0].Path != NftBootTablePath {
		t.Fatalf("second confirm rewrote the boot conf: %+v", again)
	}
	empty := NftablesBootFiles([]int{22}, "")
	if empty[1].Content != "#!/usr/sbin/nft -f\n"+NftIncludeLine+"\n" {
		t.Fatalf("created boot conf: %q", empty[1].Content)
	}
	wantCmd(t, NftablesBootCmds()[0], "systemctl", "enable", "nftables")
}

func TestSSHDropinDriverIsDistroCorrectAndReloadOnly(t *testing.T) {
	for _, tc := range []struct{ distro, unit string }{{"ubuntu", "ssh"}, {"debian", "ssh"}, {"fedora", "sshd"}, {"", "ssh"}} {
		d := SSHDropin(tc.distro, "ssh.no_password")
		if d.Files[0].Path != sshDropin || d.Files[0].Content != "PasswordAuthentication no\n" {
			t.Fatalf("drop-in = %+v", d.Files[0])
		}
		wantCmd(t, d.Post[0], "sshd", "-t")
		wantCmd(t, d.Post[1], "systemctl", "reload", tc.unit)
		if !sshReloadOnly(d.Post) {
			t.Fatalf("%s: %v restarts sshd", tc.distro, d.Post)
		}
	}
	if sshReloadOnly([]Cmd{{"systemctl", []string{"restart", "ssh"}}}) {
		t.Fatal("restart accepted")
	}
}

func TestSSHEffectiveValueVerification(t *testing.T) {
	ctx := context.Background()
	dump := "Port 22\nPasswordAuthentication yes\nPermitRootLogin yes\nPermitEmptyPasswords no\nMaxAuthTries 6\nCiphers aes256-ctr\n"
	hardened := strings.Replace(dump, "PasswordAuthentication yes", "PasswordAuthentication no", 1)
	hardened = strings.Replace(hardened, "MaxAuthTries 6", "MaxAuthTries 3", 1)
	for _, tc := range []struct {
		key, dump, want string
		ok              bool
	}{
		{"ssh.no_password", dump, "passwordauthentication is yes", false},
		{"ssh.no_password", hardened, "", true},
		{"ssh.no_root_password", dump, "permitrootlogin is yes", false},
		{"ssh.no_root_password", "PermitRootLogin prohibit-password\n", "", true},
		{"ssh.limit_auth", dump, "maxauthtries 6 is outside 1..4", false},
		{"ssh.limit_auth", hardened, "", true},
		{"ssh.limit_auth", "PermitEmptyPasswords no\n", "maxauthtries missing", false},
		{"fw.default_deny", dump, "no effective-value expectation", false},
	} {
		ok, detail := SSHValuesSatisfied(tc.key, SSHEffective(tc.dump))
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v detail=%s", tc.key, ok, detail)
		}
		if tc.want != "" && !strings.Contains(detail, tc.want) {
			t.Fatalf("%s: detail = %s, want it to contain %q", tc.key, detail, tc.want)
		}
	}
	d := SSHDropin("ubuntu", "ssh.no_password")
	weak := &fakeRunner{out: map[string]string{"sshd": dump}}
	if ok, _ := d.Verify(ctx, Env{}, weak.run); ok {
		t.Fatal("effective password login accepted")
	}
	failing := &fakeRunner{out: map[string]string{"sshd": hardened}, err: map[string]error{"sshd": errors.New("permission denied")}}
	if ok, detail := d.Verify(ctx, Env{}, failing.run); ok || !strings.Contains(detail, "sshd -T failed") {
		t.Fatalf("failing sshd -T accepted: %s", detail)
	}
}

func TestSSHApplyGuardRefusesUnprovenKeyLogin(t *testing.T) {
	items := []PreflightItem{{Key: "include", OK: true}, {Key: "sshd_t", OK: true}, {Key: "authorized_keys", OK: true}}
	if err := SSHApplyGuard(items); !errors.Is(err, ErrKeyLoginUnproven) {
		t.Fatalf("apply allowed without proven key login: %v", err)
	}
	if err := SSHApplyGuard(append(items, PreflightItem{Key: "pubkey_evidence", OK: true})); err != nil {
		t.Fatalf("guard refused a satisfied preflight: %v", err)
	}
	weak := []PreflightItem{{Key: "include", OK: true}, {Key: "sshd_t", OK: false}, {Key: "authorized_keys", OK: false}, {Key: "pubkey_evidence", OK: true}}
	if err := SSHApplyGuard(weak); err == nil || !strings.Contains(err.Error(), "sshd_t, authorized_keys") {
		t.Fatalf("guard reported %v", err)
	}
}

func TestRuntimeRevertReplaysRecordedSteps(t *testing.T) {
	ctx := context.Background()
	spool := t.TempDir()
	id := "chg_fw1"
	dir, err := WriteSnapshot("/", spool, id, []string{NftRulesetPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := PlanRuntimeRevert(spool, id, Plan{CheckKey: "fw.default_deny"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, RevertCommandsFile)); err != nil {
		t.Fatalf("no live-state steps recorded: %v", err)
	}
	f := &fakeRunner{out: map[string]string{"nft": liveNftTable}}
	if err := RuntimeRevert(ctx, spool, id, f.run); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %v", f.calls)
	}
	wantCmd(t, f.calls[0], "nft", "list", "table", "inet", "codeplo")
	wantCmd(t, f.calls[1], "nft", "delete", "table", "inet", "codeplo")

	// probe fails (the table is already gone): nothing is deleted and the revert is not an error
	gone := &fakeRunner{err: map[string]error{"nft": errors.New("No such file or directory")}}
	if err := RuntimeRevert(ctx, spool, id, gone.run); err != nil {
		t.Fatal(err)
	}
	if len(gone.calls) != 1 {
		t.Fatalf("deleted a table that was not there: %v", gone.calls)
	}

	// a failing delete is reported, so the timer journal shows the problem
	destructive := func(_ context.Context, name string, args ...string) (string, error) {
		if strings.Join(args, " ") == "delete table inet codeplo" {
			return "", errors.New("Operation not permitted")
		}
		return liveNftTable, nil
	}
	err = RuntimeRevert(ctx, spool, id, platform.Runner(destructive))
	if err == nil || !strings.Contains(err.Error(), "delete table inet codeplo") {
		t.Fatalf("failed delete reported as %v", err)
	}
	// steps recorded but no runner: refuse instead of skipping the live restore
	if err := RuntimeRevert(ctx, spool, id, nil); err == nil {
		t.Fatal("live-state steps skipped silently")
	}

	// Tier 1 records nothing and replays nothing
	tier1 := "chg_sysctl1"
	if _, err := WriteSnapshot("/", spool, tier1, []string{SysctlConfPath}); err != nil {
		t.Fatal(err)
	}
	if err := PlanRuntimeRevert(spool, tier1, Plan{CheckKey: "kern.sysctl"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(spool, tier1, RevertCommandsFile)); !os.IsNotExist(err) {
		t.Fatalf("tier 1 recorded live steps: %v", err)
	}
	clean := &fakeRunner{}
	if err := RuntimeRevert(ctx, spool, tier1, clean.run); err != nil {
		t.Fatal(err)
	}
	if len(clean.calls) != 0 {
		t.Fatalf("tier 1 ran commands: %v", clean.calls)
	}
}

func TestSSHRuntimeRevertReloadsTheRestoredConfig(t *testing.T) {
	steps := SSHRuntimeRevert("fedora")
	wantCmd(t, steps[0].Probe, "sshd", "-t")
	wantCmd(t, steps[0].Apply, "systemctl", "reload", "sshd")
}

func TestPlanCarriesDriverBodies(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFixture(t, root, "/etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\nPort 2222\n")
	probe := &fakeRunner{}
	p := &Planner{Env: Env{Root: root}, Run: probe.run}

	pl, err := p.Plan(ctx, "fw.default_deny", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if pl.Tier != 2 || len(pl.Files) != 1 || pl.Files[0].Path != NftRulesetPath {
		t.Fatalf("firewall plan = %+v", pl)
	}
	if !strings.Contains(pl.Files[0].Content, "2222") || !NftRulesetIsAdditive(pl.Files[0].Content) {
		t.Fatalf("ruleset body: %s", pl.Files[0].Content)
	}
	if pl.Diff == nil || !strings.Contains(pl.Diff.After, "table inet codeplo") {
		t.Fatalf("diff = %+v", pl.Diff)
	}
	wantCmd(t, pl.Post[0], "nft", "-f", NftRulesetPath)
	cmds := strings.Join(pl.Commands(), "\n")
	if !strings.Contains(cmds, "write "+NftRulesetPath) {
		t.Fatalf("commands lost the ruleset file: %s", cmds)
	}
	// Tier 2 never lands through the Tier 1 apply path
	if _, err := p.Apply(ctx, pl, t.TempDir(), "chg_x"); !errors.Is(err, ErrTierBlocked) {
		t.Fatalf("apply allowed a Tier 2 plan: %v", err)
	}

	pl, err = p.Plan(ctx, "ssh.no_password", Params{})
	if err != nil {
		t.Fatal(err)
	}
	if len(pl.Files) != 1 || pl.Files[0].Path != sshDropin || pl.Files[0].Content != "PasswordAuthentication no\n" {
		t.Fatalf("ssh plan files = %+v", pl.Files)
	}

	pl, err = p.Plan(ctx, "bf.protection", Params{AdminIPs: []string{"203.0.113.9"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pl.Files[0].Content, "203.0.113.9") {
		t.Fatalf("jail body: %s", pl.Files[0].Content)
	}
	if miss := Fail2banMissingIPs([]string{"203.0.113.9"}, pl.Files[0].Content); len(miss) > 0 {
		t.Fatalf("planned jail would ban %v", miss)
	}
}

func TestPreflightChecksRealState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeFixture(t, root, "/etc/ssh/sshd_config", "Include /etc/ssh/sshd_config.d/*.conf\nPort 2222\n")
	p := &Planner{Env: Env{Root: root}}

	p.Run = (&fakeRunner{out: map[string]string{"nft": liveRuleset}}).run
	items, err := p.Preflight(ctx, "fw.default_deny", "ops", []string{"203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	if !OK(items) {
		t.Fatalf("firewall preflight failed: %+v", items)
	}
	if got := byKey(items, "ssh_ports"); !strings.Contains(got, "[2222]") {
		t.Fatalf("sshd port missing: %s", got)
	}
	kept := byKey(items, "nft")
	for _, want := range []string{"ip nat", "ip filter"} {
		if !strings.Contains(kept, want) {
			t.Fatalf("existing table not reported as kept: %s", kept)
		}
	}
	if strings.Contains(kept, "flush") {
		t.Fatalf("preflight plans a flush: %s", kept)
	}
	if !strings.Contains(byKey(items, "runtime_first"), NftBootConfPath) {
		t.Fatal("runtime-first item lost")
	}

	ssh := &fakeRunner{out: map[string]string{"sshd": "Port 2222\nPasswordAuthentication yes\n"}}
	p.Run = ssh.run
	items, err = p.Preflight(ctx, "ssh.no_password", "ops", []string{"203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	if byKeyOK(items, "pubkey_evidence") {
		t.Fatal("key login proven with no auth log in the tree")
	}
	// the guard that blocks the apply is the same evidence the preflight reports
	if err := SSHApplyGuard(items); !errors.Is(err, ErrKeyLoginUnproven) {
		t.Fatalf("SSH apply allowed without proven key login: %v", err)
	}
	if !byKeyOK(items, "effective") || !strings.Contains(byKey(items, "effective"), "passwordauthentication=yes") {
		t.Fatalf("effective config item wrong: %+v", items)
	}
	if !byKeyOK(items, "reload_only") {
		t.Fatal("reload-only item failed")
	}

	// a host where sshd -T cannot answer is not verifiable
	p.Run = (&fakeRunner{err: map[string]error{"sshd": errors.New("permission denied")}}).run
	items, _ = p.Preflight(ctx, "ssh.no_password", "ops", nil)
	if byKeyOK(items, "effective") {
		t.Fatal("unreadable effective config accepted")
	}

	bf, err := p.Preflight(ctx, "bf.protection", "ops", []string{"203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	if !byKeyOK(bf, "admin_ips") || !strings.Contains(byKey(bf, "admin_ips"), "203.0.113.9") {
		t.Fatalf("ignore list item wrong: %+v", bf)
	}
}

// Applying the same plan twice leaves exactly one file with the planned body: checks are idempotent.
func TestApplyTwiceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	root, spool := t.TempDir(), t.TempDir()
	f := &fakeRunner{out: map[string]string{"systemctl": "active"}}
	p := &Planner{Env: Env{Root: root}, Run: f.run}
	admin := []string{"203.0.113.9"}
	for _, d := range []Driver{UnattendedUpgrades(), Sysctl(), Fail2ban(admin)} {
		pl, err := p.Plan(ctx, d.Key, Params{AdminIPs: admin})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := p.Apply(ctx, pl, spool, fmt.Sprintf("chg_%s_%d", d.Key, i)); err != nil {
				t.Fatalf("%s apply %d: %v", d.Key, i, err)
			}
		}
		if got := readFixture(t, root, pl.Files[0].Path); got != pl.Files[0].Content {
			t.Fatalf("%s: file on disk differs from the plan body:\n%s", d.Key, got)
		}
		if d.Key == "kern.sysctl" {
			continue // the live kernel is not virtualised by the fixture root
		}
		if ok, detail := d.Verify(ctx, Env{Root: root}, f.run); !ok {
			t.Fatalf("%s verify: %s", d.Key, detail)
		}
	}
}
