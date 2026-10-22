package providers

import "testing"

func TestCanonicalRepo(t *testing.T) {
	a := CanonicalRepo("git@GitHub.com:Org/Repo.git")
	b := CanonicalRepo("https://github.com/Org/Repo")
	if a == "" || a != b {
		t.Fatalf("ssh/https mismatch: %q vs %q", a, b)
	}
	// path case sensitive
	if CanonicalRepo("https://github.com/Org/repo") == a {
		t.Fatal("path should be case-sensitive")
	}
	if !RepoMatches("https://github.com/Org/Repo.git", "git@github.com:Org/Repo.git") {
		t.Fatal("RepoMatches should equate forms")
	}
}
