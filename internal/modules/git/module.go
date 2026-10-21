// Package git is the Phase 5 source layer: git connections, per-resource sources, ed25519 deploy keys, GitHub
// webhook verification and deploy-hook tokens. The HTTP layer is internal/api/handlers_git.go; the rules this
// package exists to enforce are that a secret is written once and never read back, that a delivery is trusted only
// when its HMAC matches, and that nothing outside a repository's own branch or tag policy can deploy.
//
// It deliberately holds no HTTP and no SQL transaction of its own: every write takes the caller's Querier, so a
// source change, its key material and its audit row commit together or not at all.
package git

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

// Querier is satisfied by both *sql.DB and *sql.Tx. Reads may take the pool; writes must take the transaction that
// also carries the audit row.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Service is the git layer's entry point. Key is the daemon's master key: it seals deploy keys, registry
// credentials and webhook secrets, and it is the only thing that can open them again.
type Service struct {
	DB  *sql.DB
	Key []byte
	// Now is injectable so a test can freeze time; it is the daemon clock in production.
	Now func() time.Time
}

func New(db *sql.DB, key []byte) *Service { return &Service{DB: db, Key: key} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ts is the wire format for every timestamp this package writes (ISO-8601, millisecond precision, UTC).
func (s *Service) ts() string { return s.now().UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) id(prefix string) string { return crypto.ULID(prefix, s.now().UnixMilli()) }

// FieldErrors is a validation failure keyed by request field; the handler turns it into the 422 envelope.
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return "validation failed" }

// NotFound and Conflict are the two non-validation failures a git operation can report. The handler maps them to
// 404 and 409 with the message the executable mock uses.
type NotFound struct{ What string }

func (e NotFound) Error() string { return e.What + " not found" }

type Conflict struct{ Msg string }

func (e Conflict) Error() string { return e.Msg }

// ErrSealedKey means a row exists but the master key cannot open it: a different database, or a key rotated out
// from under it. It is reported as a failure of the daemon, never as an empty credential.
var ErrSealedKey = errors.New("stored secret could not be decrypted with this master key")

// Sources are the three kinds the contract allows, and the only values resource_sources.kind can hold.
const (
	KindPublicGit     = "public_git"
	KindDeployKey     = "deploy_key_git"
	KindRegistry      = "registry"
	KindDeployKeyCred = "deploy_key"
)

func validKind(k string) bool {
	return k == KindPublicGit || k == KindDeployKey || k == KindRegistry
}

// deref is the nil-safe read of an optional field.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func str(s sql.NullString) *string {
	if !s.Valid || s.String == "" {
		return nil
	}
	v := s.String
	return &v
}
