// Package modules: Module contract (plan 4.3) and registry. A disabled module registers no routes and no jobs.
package modules

// T33: Registry.Register is not called by the daemon today; routes are wired from internal/api
// route tables. These types remain the planned module contract (ADR 0005) and must not be deleted
// without rewriting that ADR.

import (
	"context"
	"database/sql"
	"io/fs"

	"github.com/go-chi/chi/v5"
)

type Deps struct{ DB *sql.DB }

type Module interface {
	Key() string
	Migrations() fs.FS
	Permissions() []string
	Routes(r chi.Router, d Deps)
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type Registry struct {
	db   *sql.DB
	mods map[string]Module
}

func NewRegistry(db *sql.DB) *Registry { return &Registry{db: db, mods: map[string]Module{}} }

// Register adds an implementation. No module ships in P0/P1; later phases register here.
func (r *Registry) Register(m Module) { r.mods[m.Key()] = m }

func (r *Registry) Implemented(key string) bool { _, ok := r.mods[key]; return ok }

// Enabled reads the flag from the database (source of truth).
func (r *Registry) Enabled(ctx context.Context, key string) bool {
	var e int
	if err := r.db.QueryRowContext(ctx, `SELECT enabled FROM modules WHERE key=?`, key).Scan(&e); err != nil {
		return false
	}
	return e == 1
}
