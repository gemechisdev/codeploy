// Package jobs: durable queue persisted in SQLite, polled by a worker loop.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
)

type Handler func(ctx context.Context, payload string) error

type Queue struct {
	db       *sql.DB
	handlers map[string]Handler
}

func New(db *sql.DB) *Queue { return &Queue{db: db, handlers: map[string]Handler{}} }

func (q *Queue) Register(kind string, h Handler) { q.handlers[kind] = h }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Enqueue inserts a job; pass the caller's tx so the job commits with the state change.
func (q *Queue) Enqueue(ctx context.Context, tx *sql.Tx, kind, payload string, runAt time.Time) (string, error) {
	now := time.Now()
	id := crypto.ULID("job_", now.UnixMilli())
	_, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,kind,payload,run_at,created_at,updated_at) VALUES (?,?,?,?,?,?)`, id, kind, payload, ts(runAt), ts(now), ts(now))
	return id, err
}

// Run polls until ctx is cancelled. Jobs left 'running' by a crash are re-queued at start (handlers must be idempotent).
func (q *Queue) Run(ctx context.Context) {
	_, _ = q.db.ExecContext(ctx, `UPDATE jobs SET state='queued' WHERE state='running'`)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for q.runOne(ctx) {
			}
		}
	}
}

func (q *Queue) runOne(ctx context.Context) bool {
	var id, kind, payload string
	var attempts int
	now := ts(time.Now())
	err := q.db.QueryRowContext(ctx, `UPDATE jobs SET state='running', attempts=attempts+1, updated_at=? WHERE id=(
		SELECT id FROM jobs WHERE state='queued' AND run_at<=? ORDER BY run_at LIMIT 1) RETURNING id,kind,payload,attempts`,
		now, now).Scan(&id, &kind, &payload, &attempts)
	if err != nil {
		return false
	}
	var herr error
	if h := q.handlers[kind]; h == nil {
		herr = errors.New("no handler for job kind " + kind)
	} else {
		herr = h(ctx, payload)
	}
	state, msg := "done", ""
	if herr != nil {
		slog.Warn("job failed", "id", id, "kind", kind, "err", herr)
		state, msg = "failed", herr.Error()
		if attempts < 5 {
			state = "queued"
		}
	}
	next := ts(time.Now().Add(time.Duration(attempts*attempts) * 5 * time.Second))
	_, _ = q.db.ExecContext(ctx, `UPDATE jobs SET state=?, last_error=?, run_at=CASE WHEN ?='queued' THEN ? ELSE run_at END, updated_at=? WHERE id=?`, state, msg, state, next, ts(time.Now()), id)
	return true
}
