package extensions

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/codeplo/codeplo/internal/crypto"
)

type Extension struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Endpoint  string   `json:"endpoint_url"`
	CreatedAt string   `json:"created_at"`
	Events    []string `json:"events,omitempty"`
}

// Register creates an extension and returns the one-time plaintext signing secret (never stored).
func (s *Service) Register(ctx context.Context, name, endpoint string, events []string) (ext Extension, secret string, err error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return Extension{}, "", fmt.Errorf("name must be 1–64 characters")
	}
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return Extension{}, "", fmt.Errorf("endpoint must be an http(s) URL")
	}
	secret = "xsec_" + crypto.RandomHex(24)
	blob, err := crypto.Seal(s.Key, []byte(secret))
	if err != nil {
		return Extension{}, "", err
	}
	id, created := s.id("ext_"), s.ts()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Extension{}, "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO extensions(id,name,endpoint_url,secret_enc,created_at) VALUES (?,?,?,?,?)`,
		id, name, u.String(), blob, created); err != nil {
		return Extension{}, "", err
	}
	for _, ev := range events {
		ev = strings.TrimSpace(ev)
		if ev == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO extension_subscriptions(extension_id,event) VALUES (?,?)`, id, ev); err != nil {
			return Extension{}, "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return Extension{}, "", err
	}
	return Extension{ID: id, Name: name, Endpoint: u.String(), CreatedAt: created, Events: events}, secret, nil
}

func (s *Service) List(ctx context.Context) ([]Extension, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,name,endpoint_url,created_at FROM extensions ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Extension
	for rows.Next() {
		var e Extension
		if err := rows.Scan(&e.ID, &e.Name, &e.Endpoint, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Service) openSecret(ctx context.Context, extensionID string) ([]byte, error) {
	var blob []byte
	if err := s.DB.QueryRowContext(ctx, `SELECT secret_enc FROM extensions WHERE id=?`, extensionID).Scan(&blob); err != nil {
		return nil, err
	}
	return crypto.Open(s.Key, blob)
}

// CreateToken issues a scoped token; plaintext is returned once, only the hash is stored.
func (s *Service) CreateToken(ctx context.Context, extensionID, name, scopes string) (tokenID, plaintext string, err error) {
	plaintext = "xtok_" + crypto.RandomHex(24)
	tokenID = s.id("xtk_")
	_, err = s.DB.ExecContext(ctx, `INSERT INTO extension_tokens(id,extension_id,name,hash,scopes,created_at) VALUES (?,?,?,?,?,?)`,
		tokenID, extensionID, name, crypto.HashToken(plaintext), scopes, s.ts())
	return tokenID, plaintext, err
}

// ResolveToken returns extension id and scopes for a bearer token, or sql.ErrNoRows.
func (s *Service) ResolveToken(ctx context.Context, raw string) (extensionID, scopes string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT extension_id, scopes FROM extension_tokens WHERE hash=?`, crypto.HashToken(raw)).
		Scan(&extensionID, &scopes)
	if err == nil {
		_, _ = s.DB.ExecContext(ctx, `UPDATE extension_tokens SET last_used_at=? WHERE hash=?`, s.ts(), crypto.HashToken(raw))
	}
	return
}
