package builders

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBuildDetectsNodeAndOverrides(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x","scripts":{"start":"node i.js","build":"echo hi"},"dependencies":{"express":"4"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ResolveBuild(dir, BuildOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pack != "node" {
		t.Fatalf("pack=%s", plan.Pack)
	}
	if !strings.Contains(plan.InstallCommand, "npm") {
		t.Fatalf("install=%q", plan.InstallCommand)
	}
	// Override start
	plan2, err := ResolveBuild(dir, BuildOverrides{StartCommand: "node server.js", InstallCommand: "npm install"})
	if err != nil {
		t.Fatal(err)
	}
	if plan2.StartCommand != "node server.js" {
		t.Fatalf("start=%q", plan2.StartCommand)
	}
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile.codegen")); err != nil {
		t.Fatal(err)
	}
}

func TestResolveBuildUsesExistingDockerfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ResolveBuild(dir, BuildOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pack != "dockerfile" {
		t.Fatalf("pack=%s", plan.Pack)
	}
}

func TestResolveBuildStatic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>hi</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ResolveBuild(dir, BuildOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Pack != "static" {
		t.Fatalf("pack=%s", plan.Pack)
	}
}
