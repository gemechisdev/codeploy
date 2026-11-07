// Package app wires config, store, crypto and the API server; also holds helpers shared by the daemon and CLI.
package app

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/store"
)

var (
	Version = "0.1.0-dev"
	Commit  = "none"
	BuiltAt = "unknown"
)

const SetupTokenTTL = time.Hour

// Open creates the data dir, opens the DB, migrates and loads the master key.
func Open(c config.Config) (*sql.DB, []byte, error) {
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, nil, err
	}
	db, err := store.Open(c.DBPath())
	if err != nil {
		return nil, nil, err
	}
	if err := store.Migrate(db); err != nil {
		return nil, nil, err
	}
	key, err := crypto.LoadOrCreateMasterKey(c.MasterKeyPath())
	return db, key, err
}

// IssueSetupToken mints a 256-bit one-time token (stored hashed, expires in 1 h).
// Before an owner exists it is used by POST /setup/admin. After an owner exists but while
// setup_state.step is not "done", the same token is accepted by POST /setup/resume to re-bind
// a setup-scoped session (e.g. after the browser cookie was cleared).
func IssueSetupToken(ctx context.Context, db *sql.DB) (string, error) {
	tok := crypto.RandomHex(32)
	err := store.InTx(ctx, db, func(tx *sql.Tx) error {
		var step string
		if err := tx.QueryRow(`SELECT step FROM setup_state WHERE id=1`).Scan(&step); err != nil {
			return err
		}
		if step == "done" {
			return errors.New("setup is finished: an owner already exists")
		}
		if _, err := tx.Exec(`UPDATE setup_state SET token_hash=?, token_expires_at=? WHERE id=1`, crypto.HashToken(tok), time.Now().Add(SetupTokenTTL).UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		action := "setup.token_issued"
		if step != "admin" && step != "" {
			action = "setup.token_reissued"
		}
		_, err := audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "system", Action: action, TargetType: "system", TargetID: "setup"})
		return err
	})
	return tok, err
}

// EnsureSetupToken issues a token on daemon start when setup is pending and no valid token exists.
func EnsureSetupToken(ctx context.Context, db *sql.DB) (tok string, issued bool, err error) {
	var n int
	var th, exp sql.NullString
	if err = db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM users), token_hash, token_expires_at FROM setup_state WHERE id=1`).Scan(&n, &th, &exp); err != nil || n > 0 {
		return "", false, err
	}
	if t, e := time.Parse(time.RFC3339Nano, exp.String); th.Valid && e == nil && time.Now().Before(t) {
		return "", false, nil // a valid token exists; it is only shown when minted
	}
	tok, err = IssueSetupToken(ctx, db)
	return tok, err == nil, err
}
