package main

import (
	"regexp"
	"testing"
)

// Proof for the public release source: both the first-install script and `codeplo update` must
// resolve to the official repository by default, and the cosign identity must stay pinned to the
// release workflow. These are pure helpers, so the test needs no network and writes nothing.
func clearReleaseEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"CODEPLO_REPO", "CODEPLO_VERSION", "CODEPLO_BASE_URL"} {
		t.Setenv(key, "")
	}
}

func TestReleaseBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr bool
	}{
		{
			name: "official latest release by default",
			want: "https://github.com/gemechisdev/codeploy/releases/latest/download",
		},
		{
			name: "pinned official tag",
			env:  map[string]string{"CODEPLO_VERSION": "v1.2.0"},
			want: "https://github.com/gemechisdev/codeploy/releases/download/v1.2.0",
		},
		{
			name: "pinned prerelease tag",
			env:  map[string]string{"CODEPLO_VERSION": "v1.2.0-rc.1"},
			want: "https://github.com/gemechisdev/codeploy/releases/download/v1.2.0-rc.1",
		},
		{
			name: "fork override",
			env:  map[string]string{"CODEPLO_REPO": "acme/codeploy-fork", "CODEPLO_VERSION": "v2.0.0"},
			want: "https://github.com/acme/codeploy-fork/releases/download/v2.0.0",
		},
		{
			name: "https mirror override trims one trailing slash",
			env:  map[string]string{"CODEPLO_BASE_URL": "https://mirror.example/releases/v1.2.0/"},
			want: "https://mirror.example/releases/v1.2.0",
		},
		{
			name:    "path traversal in version is refused",
			env:     map[string]string{"CODEPLO_VERSION": "../v1.2.0"},
			wantErr: true,
		},
		{
			name:    "double dot in version is refused",
			env:     map[string]string{"CODEPLO_VERSION": "v1..2"},
			wantErr: true,
		},
		{
			name:    "path traversal in repo is refused",
			env:     map[string]string{"CODEPLO_REPO": "acme/../evil", "CODEPLO_VERSION": "v1.2.0"},
			wantErr: true,
		},
		{
			name:    "non-https base URL is refused",
			env:     map[string]string{"CODEPLO_BASE_URL": "http://mirror.example/releases"},
			wantErr: true,
		},
		{
			name:    "base URL with query is refused",
			env:     map[string]string{"CODEPLO_BASE_URL": "https://mirror.example/releases?token=1"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearReleaseEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := releaseBaseURL()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got URL %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("releaseBaseURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("releaseBaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDefaultCosignIdentity(t *testing.T) {
	official := regexp.MustCompile(defaultCosignIdentity(defaultReleaseRepo))
	valid := "https://github.com/gemechisdev/codeploy/.github/workflows/release.yml@refs/tags/v1.2.0"
	if !official.MatchString(valid) {
		t.Fatalf("official identity regexp does not match %q", valid)
	}
	for _, bad := range []string{
		"https://github.com/evil/codeploy/.github/workflows/release.yml@refs/tags/v1.2.0",
		"https://github.com/gemechisdev/codeploy/.github/workflows/ci.yml@refs/tags/v1.2.0",
		"https://github.com/gemechisdev/codeploy/.github/workflows/release.yml@refs/heads/main",
	} {
		if official.MatchString(bad) {
			t.Fatalf("official identity regexp wrongly matches %q", bad)
		}
	}

	// A fork repo name containing a dot must stay literal, not become a regexp wildcard.
	fork := regexp.MustCompile(defaultCosignIdentity("acme/my.repo"))
	if !fork.MatchString("https://github.com/acme/my.repo/.github/workflows/release.yml@refs/tags/v2.0.0") {
		t.Fatal("fork identity regexp does not match its own release workflow")
	}
	if fork.MatchString("https://github.com/acme/myXrepo/.github/workflows/release.yml@refs/tags/v2.0.0") {
		t.Fatal("fork identity regexp treated a dot as a wildcard")
	}
}

func TestReleaseVersionDefault(t *testing.T) {
	clearReleaseEnv(t)
	ver, err := releaseVersion()
	if err != nil {
		t.Fatalf("releaseVersion: %v", err)
	}
	if ver != latestReleaseVersion {
		t.Fatalf("releaseVersion = %q, want %q", ver, latestReleaseVersion)
	}
	if repo := releaseRepo(); repo != defaultReleaseRepo {
		t.Fatalf("releaseRepo = %q, want %q", repo, defaultReleaseRepo)
	}
}
