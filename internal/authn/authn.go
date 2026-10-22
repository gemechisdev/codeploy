// Package authn: password hashing (argon2id), TOTP, recovery codes.
package authn

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"

	"github.com/codeplo/codeplo/internal/crypto"
)

// argon2id parameters from plan 3.5: m=32 MiB, t=3, p=1.
const (
	memKiB  = 32 * 1024
	iters   = 3
	threads = 1
	keyLen  = 32
)

const MinPasswordLen = 12

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	h := argon2.IDKey([]byte(pw), salt, iters, memKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memKiB, iters, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h)), nil
}

func VerifyPassword(pw, encoded string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 6 || p[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var th uint8
	if _, err := fmt.Sscanf(p[3], "m=%d,t=%d,p=%d", &m, &t, &th); err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(p[4])
	want, err2 := base64.RawStdEncoding.DecodeString(p[5])
	if err1 != nil || err2 != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, th, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyVerify burns comparable CPU for unknown users (no enumeration by timing).
func DummyVerify() { argon2.IDKey([]byte("x"), make([]byte, 16), iters, memKiB, threads, keyLen) }

func NewTOTP(email string) (secret, uri string, err error) {
	k, err := totp.Generate(totp.GenerateOpts{Issuer: "codeplo", AccountName: email})
	if err != nil {
		return "", "", err
	}
	return k.Secret(), k.URL(), nil
}

func ValidTOTP(secret, code string) bool { return totp.Validate(strings.TrimSpace(code), secret) }

// NewRecoveryCodes returns n display codes (xxxx-xxxx-xxxx) and their SHA-256 hashes.
func NewRecoveryCodes(n int) (codes, hashes []string) {
	for i := 0; i < n; i++ {
		c := crypto.RandomHex(2) + "-" + crypto.RandomHex(2) + "-" + crypto.RandomHex(2)
		codes = append(codes, c)
		hashes = append(hashes, crypto.HashToken(NormalizeRecovery(c)))
	}
	return
}

func NormalizeRecovery(c string) string { return strings.ToLower(strings.TrimSpace(c)) }

// ReplayGuard rejects a TOTP code already accepted for the same subject within its validity window.
//
// T35: the set is in-memory on purpose. A daemon restart forgets it, which is bounded by the 30 s TOTP
// step plus skew (~90 s prune). Persisting codes would need a new migration and a prune job for a
// window that already closes on its own; the residual risk is one reused code after an immediate
// restart inside that window, which requires both the OTP and a compromised session path.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func NewReplayGuard() *ReplayGuard { return &ReplayGuard{seen: map[string]time.Time{}} }

// Fresh reports whether (subject, code) is unused, and marks it used.
func (g *ReplayGuard) Fresh(subject, code string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for k, t := range g.seen {
		if now.Sub(t) > 90*time.Second {
			delete(g.seen, k)
		}
	}
	k := subject + ":" + strings.TrimSpace(code)
	if _, dup := g.seen[k]; dup {
		return false
	}
	g.seen[k] = now
	return true
}
