package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDistroFrom(t *testing.T) {
	p := filepath.Join(t.TempDir(), "os-release")
	_ = os.WriteFile(p, []byte("NAME=\"Ubuntu\"\nID=ubuntu\n"), 0o644)
	if got := distroFrom(p); got != "ubuntu" || !SupportedDistro(got) {
		t.Fatalf("got %q", got)
	}
	if distroFrom(filepath.Join(t.TempDir(), "missing")) != "" || SupportedDistro("alpine") {
		t.Fatal("unexpected")
	}
}
