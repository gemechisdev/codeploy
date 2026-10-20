package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorsFrom(t *testing.T) {
	d := t.TempDir()
	pw := filepath.Join(d, "passwd")
	gr := filepath.Join(d, "group")
	// Home dir is relative: on Windows an absolute path contains a colon, which would split the
	// colon-delimited passwd line and make the test platform-specific.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(d); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(wd) })
	os.WriteFile(pw, []byte("root:x:0:0::/root:/bin/bash\nubuntu:x:1000:1000::home/ubuntu:/bin/bash\nbackup:x:1002:1002::/nonexistent:/usr/sbin/nologin\nnobody:x:65534:65534::/:/bin/false\nBad:x:1003:1003::/h:/bin/bash\n"), 0o600)
	os.WriteFile(gr, []byte("sudo:x:27:ubuntu\n"), 0o600)
	os.MkdirAll(filepath.Join(d, "home", "ubuntu", ".ssh"), 0o700)
	os.WriteFile(filepath.Join(d, "home", "ubuntu", ".ssh", "authorized_keys"), []byte("ssh-ed25519 AAAA x\n"), 0o600)
	got := operatorsFrom(pw, gr)
	if len(got) != 1 || got[0].Username != "ubuntu" || !got[0].Sudo || !got[0].HasAuthorizedKeys {
		t.Fatalf("unexpected candidates: %+v", got)
	}
}
