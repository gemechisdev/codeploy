package git

import "testing"

func TestMatchImage(t *testing.T) {
	if !MatchImage("app:*", "app:latest") {
		t.Fatal("tag form")
	}
	digest := "app:v1@sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if !MatchImage("app:*", digest) {
		t.Fatal("digest form")
	}
	if MatchImage("app:*", "app-evil:latest") {
		t.Fatal("prefix smuggling")
	}
	if MatchImage("app:*", "other:latest") {
		t.Fatal("different name")
	}
}
