package deploy

import "testing"

func TestRedactorMasksSecrets(t *testing.T) {
	r := NewRedactor([]string{"supersecret", "short", "supersecret-extra"})
	got := r.Redact("token=supersecret-extra and supersecret and short")
	// longer first; short below MinSecretLen is not in the set
	if got != "token=*** and *** and short" {
		t.Fatalf("got %q", got)
	}
	if NewRedactor(nil).Redact("x") != "x" {
		t.Fatal("nil-ish empty redactor")
	}
	var nilR *Redactor
	if nilR.Redact("keep") != "keep" {
		t.Fatal("nil redactor must pass through")
	}
}
