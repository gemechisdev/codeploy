package crypto

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTripAndTamper(t *testing.T) {
	key, err := LoadOrCreateMasterKey(filepath.Join(t.TempDir(), "sub", "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := Seal(key, []byte("JBSWY3DPEHPK3PXP"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("JBSWY3DP")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := Open(key, blob)
	if err != nil || string(got) != "JBSWY3DPEHPK3PXP" {
		t.Fatalf("round trip failed: %q %v", got, err)
	}
	blob[len(blob)-1] ^= 1
	if _, err := Open(key, blob); err == nil {
		t.Fatal("tampered ciphertext must not open")
	}
}

func TestMasterKeyIsStable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	a, _ := LoadOrCreateMasterKey(p)
	b, _ := LoadOrCreateMasterKey(p)
	if !bytes.Equal(a, b) || len(a) != 32 {
		t.Fatal("master key must persist and be 32 bytes")
	}
}

func TestHashTokenAndULID(t *testing.T) {
	if HashToken("a") == HashToken("b") || len(HashToken("a")) != 64 {
		t.Fatal("bad token hash")
	}
	a, b := ULID("usr_", 1000), ULID("usr_", 2000)
	if !strings.HasPrefix(a, "usr_") || len(a) != 4+26 || a[:14] >= b[:14] {
		t.Fatalf("ULIDs must be 26 chars and time ordered: %s %s", a, b)
	}
}
