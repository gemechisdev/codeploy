// Package crypto: master key, XChaCha20-Poly1305 secrets, token hashing, ULIDs.
package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/crypto/chacha20poly1305"
)

// LoadOrCreateMasterKey reads a 32-byte key from path (0600), creating it on first run.
func LoadOrCreateMasterKey(path string) ([]byte, error) {
	if b, err := os.ReadFile(path); err == nil {
		if len(b) != chacha20poly1305.KeySize {
			return nil, errors.New("master key has wrong length")
		}
		return b, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	k := make([]byte, chacha20poly1305.KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, os.WriteFile(path, k, 0o600)
}

// Seal encrypts plaintext; output is nonce||ciphertext.
func Seal(key, plaintext []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	n := make([]byte, a.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return a.Seal(n, n, plaintext, nil), nil
}

// Open decrypts the output of Seal.
func Open(key, blob []byte) ([]byte, error) {
	a, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(blob) < a.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	return a.Open(nil, blob[:a.NonceSize()], blob[a.NonceSize():], nil)
}

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// HashToken is the hex SHA-256 used to store session, API, setup and recovery tokens.
func HashToken(t string) string { s := sha256.Sum256([]byte(t)); return hex.EncodeToString(s[:]) }

func Equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

const crock = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// ULID returns a Crockford ULID (time-ordered) with an optional prefix.
func ULID(prefix string, nowMs int64) string {
	var tb [10]byte
	t := uint64(nowMs)
	for i := 9; i >= 0; i-- {
		tb[i] = crock[t%32]
		t /= 32
	}
	r := make([]byte, 16)
	_, _ = rand.Read(r)
	for i := range r {
		r[i] = crock[r[i]%32]
	}
	return prefix + string(tb[:]) + string(r)
}
