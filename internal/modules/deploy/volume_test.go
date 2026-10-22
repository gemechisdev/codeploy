package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoveVolumeDirConfinement(t *testing.T) {
	root := t.TempDir()
	e := &Engine{cfg: Config{VolumesRoot: root}.withDefaults(root)}
	// empty / bad slug
	if err := e.RemoveVolumeDir(""); err == nil || !strings.Contains(err.Error(), "not a resource slug") {
		t.Fatalf("empty slug: %v", err)
	}
	if err := e.RemoveVolumeDir("../etc"); err == nil || !strings.Contains(err.Error(), "not a resource slug") {
		t.Fatalf("path slug: %v", err)
	}
	// valid slug, missing dir is ok
	if err := e.RemoveVolumeDir("app-api"); err != nil {
		t.Fatalf("missing dir should succeed: %v", err)
	}
	// valid remove
	dir := filepath.Join(root, "app-api")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := e.RemoveVolumeDir("app-api"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("dir should be gone")
	}
}
