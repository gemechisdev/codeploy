// Package extensions implements out-of-process extension registration, event delivery and scoped actions (P7/J4).
// There is no HTTP route surface (T53): the CLI and daemon own this package.
package extensions

import (
	"context"
	"database/sql"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

type Service struct {
	DB  *sql.DB
	Key []byte
	Now func() time.Time
	// HTTPDo posts a signed delivery body; tests inject a fake.
	HTTPDo func(ctx context.Context, url string, headers map[string]string, body []byte) (status int, err error)
}

func New(db *sql.DB, key []byte) *Service {
	return &Service{DB: db, Key: key, Now: time.Now}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ts() string {
	return s.now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func (s *Service) id(prefix string) string {
	return crypto.ULID(prefix, s.now().UnixMilli())
}

// OutboundEnabled is the feature flag gate for delivery attempts.
func (s *Service) OutboundEnabled(ctx context.Context) bool {
	var e int
	if err := s.DB.QueryRowContext(ctx, `SELECT enabled FROM feature_flags WHERE key='extensions.outbound'`).Scan(&e); err != nil {
		return false
	}
	return e == 1
}
