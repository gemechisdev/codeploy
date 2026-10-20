package authn

import (
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"
)

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=32768,t=3,p=1$") {
		t.Fatalf("unexpected parameters: %s", h)
	}
	if !VerifyPassword("correct horse battery", h) || VerifyPassword("wrong", h) || VerifyPassword("x", "garbage") {
		t.Fatal("verification wrong")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("salts must differ")
	}
}

func TestTOTPAndRecoveryCodes(t *testing.T) {
	secret, uri, err := NewTOTP("a@b.co")
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Fatalf("%v %s", err, uri)
	}
	code, _ := totp.GenerateCode(secret, time.Now())
	if !ValidTOTP(secret, code) || ValidTOTP(secret, "000000") && code != "000000" {
		t.Fatal("totp validation wrong")
	}
	codes, hashes := NewRecoveryCodes(8)
	if len(codes) != 8 || len(hashes) != 8 || codes[0] == codes[1] {
		t.Fatal("expected 8 distinct codes")
	}
	if strings.Contains(hashes[0], codes[0]) || NormalizeRecovery(" AB-CD ") != "ab-cd" {
		t.Fatal("hashes must not contain codes; normalisation lowercases")
	}
}
