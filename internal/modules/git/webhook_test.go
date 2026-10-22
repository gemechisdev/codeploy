package git

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	secret := []byte("whsec_test")
	body := []byte(`{"ref":"refs/heads/main"}`)
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !VerifySignature(secret, body, good) {
		t.Fatal("good signature must pass")
	}
	if VerifySignature(secret, body, "sha256=deadbeef") {
		t.Fatal("wrong signature must fail")
	}
	if VerifySignature(secret, body, "sha1=abc") {
		t.Fatal("wrong algo must fail")
	}
	if VerifySignature(secret, append(body, 'x'), good) {
		t.Fatal("tampered body must fail")
	}
}

func TestSkipDeployMessage(t *testing.T) {
	if !SkipMarker("fix: thing [skip deploy]") {
		t.Fatal("should detect skip")
	}
	if SkipMarker("deploy the thing") {
		t.Fatal("should not false positive")
	}
}
