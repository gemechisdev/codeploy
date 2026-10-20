package hardening

import (
	"os"
	"path/filepath"
	"testing"
)

func snapshot(t *testing.T, root string) map[string]string {
	m := map[string]string{}
	_ = filepath.Walk(root, func(p string, i os.FileInfo, err error) error {
		if err == nil && !i.IsDir() {
			b, _ := os.ReadFile(p)
			m[p] = string(b)
		} else if err == nil {
			m[p] = "<dir>"
		}
		return nil
	})
	return m
}

// Audit mode makes zero changes: the fixture tree is byte-identical before and after.
func TestAuditIsReadOnly(t *testing.T) {
	root := t.TempDir()
	w := func(p, c string) {
		f := filepath.Join(root, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(f), 0o755)
		_ = os.WriteFile(f, []byte(c), 0o644)
	}
	w("etc/ssh/sshd_config", "PasswordAuthentication yes\n")
	w("etc/passwd", "root:x:0:0::/root:/bin/bash\nops:x:1000:1000::/home/ops:/bin/bash\n")
	w("etc/group", "sudo:x:27:ops\n")
	w("etc/apt/apt.conf.d/20auto-upgrades", "APT::Periodic::Unattended-Upgrade \"1\";\n")
	before := snapshot(t, root)
	cs := Run(Env{Root: root}, map[string]bool{})
	after := snapshot(t, root)
	if len(before) != len(after) {
		t.Fatal("tree changed")
	}
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed", k)
		}
	}
	got := map[string]Status{}
	for _, c := range cs {
		got[c.Key] = c.Status
	}
	if got["acct.operator"] != Pass || got["upd.unattended"] != Pass || got["ssh.no_password"] != Fail {
		t.Fatalf("unexpected results: %v", got)
	}
	if Score(cs) < 0 || Score(cs) > 100 {
		t.Fatal("score out of range")
	}
}
