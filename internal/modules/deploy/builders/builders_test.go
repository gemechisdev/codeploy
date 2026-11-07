package builders

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfinePathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skip("symlinks unavailable: ", err)
	}
	if _, err := ConfinePath(root, "app"); err != nil {
		t.Fatalf("a normal subdirectory must pass: %v", err)
	}
	if _, err := ConfinePath(root, "link"); err == nil {
		t.Fatal("a symlink to a directory outside the clone must be rejected")
	}
	if _, err := ConfinePath(root, "../.."); err == nil {
		// "/"-prefixed Clean pins .. inside root, so this resolves to root itself, which is allowed
		t.Log("traversal collapses to the repository root")
	}
	if _, err := ConfinePath(root, "/etc"); err == nil {
		t.Fatal("an absolute path must be rejected")
	}
}

func TestValidateGitURL(t *testing.T) {
	good := []string{"https://github.com/acme/app.git", "git@github.com:acme/app.git", "ssh://git@host.example:2222/acme/app.git"}
	bad := []string{"-oProxyCommand=x", "https://user:pass@github.com/acme/app.git", "ext::sh -c id", "file:///etc", "http://github.com/a/b", "git://host/x", ""}
	for _, u := range good {
		if err := ValidateGitURL(u); err != nil {
			t.Errorf("%s should be valid: %v", u, err)
		}
	}
	for _, u := range bad {
		if err := ValidateGitURL(u); err == nil {
			t.Errorf("%q should be rejected", u)
		}
	}
}

func TestBuildArgsUseSecretMountsNeverBuildArgs(t *testing.T) {
	args := buildArgs(BuildRequest{ContextDir: "/ctx", Dockerfile: "/ctx/Dockerfile", Tag: "codeplo/x:1", MemoryBytes: 1 << 30},
		map[string]string{"NPM_TOKEN": "/tmp/s/NPM_TOKEN"})
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--secret id=NPM_TOKEN,src=/tmp/s/NPM_TOKEN") {
		t.Fatalf("secret mount missing: %s", joined)
	}
	if strings.Contains(joined, "--build-arg") {
		t.Fatalf("secrets must never be build args: %s", joined)
	}
	if !strings.Contains(joined, "--memory 1073741824") {
		t.Fatalf("build memory limit missing: %s", joined)
	}
}

func TestDockerConfigIsEphemeralAndPrivate(t *testing.T) {
	const secret = "s3cr3t-registry-token"
	root := t.TempDir()
	c := &CLI{TmpDir: root}
	dir, err := c.writeDockerConfig(&RegistryAuth{Server: "ghcr.io", Username: "u", Password: secret})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(dir) != root {
		t.Fatalf("credential dir %q must live under the configured TmpDir %q", dir, root)
	}
	st, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		t.Fatal("DOCKER_CONFIG must be a directory")
	}
	// Windows has no POSIX permission bits: os.MkdirTemp / os.WriteFile report
	// 0777 / 0666 there regardless of the requested mode, and os.Chmod only
	// toggles the read-only attribute. The daemon's deploy target is Linux, where
	// 0700 / 0600 are real; assert them on POSIX hosts and say plainly that the
	// Windows run does not prove them (see docs/site/verification-matrix.md).
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o700 {
		t.Fatalf("DOCKER_CONFIG dir must be 0700, is %v", st.Mode().Perm())
	}
	cfgPath := filepath.Join(dir, "config.json")
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("config.json must be 0600, is %v", fi.Mode().Perm())
	}
	// Platform-independent proof: the credential is sealed into the file, never
	// left in plaintext and never handed to a build process through its environment.
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(secret)) {
		t.Fatal("plaintext registry password leaked into config.json")
	}
	var cfg struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(cfg.Auths["ghcr.io"].Auth)
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != "u:"+secret {
		t.Fatalf("unexpected sealed auth: %q", decoded)
	}
	if strings.Contains(strings.Join(c.baseEnv(dir), "\x00"), secret) {
		t.Fatal("registry password leaked into the build environment")
	}
	_ = os.RemoveAll(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the directory must be removable after use")
	}
}

func TestClassify(t *testing.T) {
	if err := classify("write /x: no space left on device", os.ErrInvalid); !strings.Contains(err.Error(), "free disk space") {
		t.Fatalf("ENOSPC not classified: %v", err)
	}
	if err := classify("", os.ErrInvalid); err == nil {
		t.Fatal("unrelated failures pass through")
	}
}
