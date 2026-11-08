package builders

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDockerfileDetectsNodeAndStatic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"start":"node i.js"},"dependencies":{"express":"4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := EnsureDockerfile(dir, "Dockerfile", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "node" {
		t.Fatalf("pack=%s want node", p.Name)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile.codegen")); err != nil {
		t.Fatal(err)
	}

	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	p2, err := EnsureDockerfile(dir2, "Dockerfile", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Name != "static" {
		t.Fatalf("pack=%s want static", p2.Name)
	}
}

func TestEnsureDockerfileUsesExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := EnsureDockerfile(dir, "Dockerfile", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "dockerfile" {
		t.Fatalf("pack=%s", p.Name)
	}
}
