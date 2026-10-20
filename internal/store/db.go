// Package store opens SQLite (WAL, pure Go), runs forward-only goose migrations and seeds.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"

	"github.com/codeplo/codeplo/internal/authz"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (and creates) the database at path with WAL, FK enforcement and immediate write transactions.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

// Migrate applies all pending migrations (forward-only) and the idempotent seeds.
func Migrate(db *sql.DB) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return err
	}
	if err := goose.Up(db, "migrations"); err != nil {
		return err
	}
	return Seed(context.Background(), db)
}

// Seed inserts role_permissions from the registry on first run only, so later admin edits to builtin roles are preserved.
func Seed(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM role_permissions`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		for role, perms := range authz.RolePermissions() {
			for _, p := range perms {
				if _, err := tx.Exec(`INSERT OR IGNORE INTO role_permissions(role, permission) VALUES (?,?)`, role, p); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// InTx runs fn in a transaction and commits if fn returns nil.
func InTx(ctx context.Context, db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
