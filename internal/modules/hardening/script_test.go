package hardening

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func needBash(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
}

func runScript(t *testing.T, body string) (string, error) {
	t.Helper()
	out, err := exec.Command("bash", "-c", body).CombinedOutput()
	return string(out), err
}

func TestShQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/etc/x.conf": "/etc/x.conf", "-o": "-o", "Dpkg::Options::=--force-confold": "Dpkg::Options::=--force-confold",
		"a b": "'a b'", "it's": `'it'\''s'`, "": "''", "$HOME": "'$HOME'",
	} {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %s, want %s", in, got, want)
		}
	}
	if got := (Cmd{"systemctl", []string{"reload", "ssh"}}).shell(); got != "systemctl reload ssh </dev/null" {
		t.Errorf("shell() = %q", got)
	}
}

func tmpPlan(t *testing.T) (Plan, string, string) {
	t.Helper()
	root := t.TempDir()
	existing := filepath.Join(root, "etc", "app.conf")
	fresh := filepath.Join(root, "etc", "conf.d", "60-new.conf")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Plan{CheckKey: "kern.sysctl", Tier: 1, Files: []FileChange{
		{Path: existing, Content: "new line one\nnew 'quoted' $VAR `tick`\n", Mode: 0o644},
		{Path: fresh, Content: "CODEPLO_FILE_1\nstill fine\n", Mode: 0o640}, // a body line equal to the delimiter
	}, Post: []Cmd{{"true", nil}}}, existing, fresh
}

func TestScriptAppliesBacksUpAndUndoes(t *testing.T) {
	needBash(t)
	pl, existing, fresh := tmpPlan(t)
	backups := filepath.Join(t.TempDir(), "backups")
	out, err := runScript(t, pl.ScriptBody("ubuntu", backups, true))
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(existing); string(b) != pl.Files[0].Content {
		t.Fatalf("existing file = %q", b)
	}
	if b, _ := os.ReadFile(fresh); string(b) != pl.Files[1].Content {
		t.Fatalf("fresh file = %q", b)
	}
	if st, _ := os.Stat(existing); st.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v", st.Mode())
	}
	if st, _ := os.Stat(fresh); st.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v", st.Mode())
	}
	if _, err := os.Stat(existing + ".codeplo-tmp"); err == nil {
		t.Error("temp file left behind")
	}
	m := regexp.MustCompile(`sh (\S+/revert\.sh)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no undo path in output: %s", out)
	}
	if o, err := exec.Command("sh", m[1]).CombinedOutput(); err != nil {
		t.Fatalf("undo failed: %v\n%s", err, o)
	}
	if b, _ := os.ReadFile(existing); string(b) != "old\n" {
		t.Errorf("undo did not restore: %q", b)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Error("undo did not remove the file the plan created")
	}
}

func TestScriptFailureUndoesAndExitsNonZero(t *testing.T) {
	needBash(t)
	pl, existing, fresh := tmpPlan(t)
	pl.Post = []Cmd{{"false", nil}}
	out, err := runScript(t, pl.ScriptBody("ubuntu", filepath.Join(t.TempDir(), "b"), true))
	if err == nil {
		t.Fatalf("a failing post command must fail the script:\n%s", out)
	}
	if b, _ := os.ReadFile(existing); string(b) != "old\n" {
		t.Errorf("failure did not restore the original: %q\n%s", b, out)
	}
	if _, err := os.Stat(fresh); err == nil {
		t.Errorf("failure left the new file behind\n%s", out)
	}
}

func TestScriptTierTwoArmsDeadManAndUndoesLiveState(t *testing.T) {
	pl := Plan{CheckKey: "fw.default_deny", Tier: 2, Files: Nftables([]int{22}).Files, Post: Nftables([]int{22}).Post}
	body := pl.ScriptBody("ubuntu", BackupRoot, true)
	for _, want := range []string{"systemd-run --quiet --unit=", "--on-active=300s", "nft delete table inet codeplo", "nft -f /var/lib/codeplo/plan/codeplo.nft </dev/null", "systemctl stop $U.timer"} {
		if !strings.Contains(body, want) {
			t.Errorf("tier 2 script lacks %q:\n%s", want, body)
		}
	}
	t1 := Plan{CheckKey: "kern.sysctl", Tier: 1, Files: Sysctl().Files, Post: Sysctl().Post}
	if strings.Contains(t1.ScriptBody("ubuntu", BackupRoot, true), "systemd-run") {
		t.Error("tier 1 cannot cut access and must not arm a timer")
	}
}

func TestEveryDriverRendersOneValidCommand(t *testing.T) {
	needBash(t)
	drv := map[string]Driver{
		"upd.unattended": UnattendedUpgrades(), "kern.sysctl": Sysctl(), "bf.protection": Fail2ban([]string{"203.0.113.9"}),
		"fw.default_deny": Nftables([]int{22, 2222}),
		"ssh.no_password": SSHDropin("ubuntu", "ssh.no_password"), "ssh.no_root_password": SSHDropin("ubuntu", "ssh.no_root_password"), "ssh.limit_auth": SSHDropin("ubuntu", "ssh.limit_auth"),
	}
	for key, d := range drv {
		tier, _ := tierOf(key)
		pl := Plan{CheckKey: key, Tier: tier, Pre: d.Pre, Files: d.Files, Post: d.Post}
		cmds := pl.Commands()
		if len(cmds) != 1 {
			t.Fatalf("%s: want exactly one command, got %d", key, len(cmds))
		}
		if !strings.HasPrefix(cmds[0], "sudo bash <<'CODEPLO_APPLY'\n") || !strings.HasSuffix(cmds[0], "\nCODEPLO_APPLY") {
			t.Errorf("%s: not wrapped as one sudo command:\n%s", key, cmds[0])
		}
		f := filepath.Join(t.TempDir(), "cmd.sh")
		if err := os.WriteFile(f, []byte(cmds[0]), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
			t.Errorf("%s: shell syntax error: %v\n%s\n%s", key, err, out, cmds[0])
		}
		if strings.Contains(cmds[0], "write /") {
			t.Errorf("%s: still prints pseudo write lines", key)
		}
	}
	if got := (Plan{CheckKey: "acct.operator", Tier: 1}).Commands(); len(got) != 0 {
		t.Errorf("a plan with nothing to run must have no commands: %v", got)
	}
}
