// Package terminal implements plan P3: browser shells on the host (PTY) and in containers (exec attach),
// the single-use tickets that authorize a WebSocket upgrade, and the session history the panel lists.
// This file holds the parts both the API handlers and the host/container shells need: settings, the session
// row, and its store.
package terminal

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

// TicketTTL is how long an issued ticket stays usable. Short by contract (plan P3: "<= 30 s") because the
// ticket is the only credential on the WebSocket URL, where cookies and headers cannot be inspected later.
const TicketTTL = 30 * time.Second

// ErrLimit rejects an Open whose user already has max concurrent sessions.
var ErrLimit = errors.New("terminal session limit reached")

// Config is the terminal's settings view, resolved once per request so a settings change takes effect on the
// next shell without restarting the daemon.
type Config struct {
	AllowRoot     bool
	Record        bool
	IdleTimeout   time.Duration
	MaxPerUser    int
	TicketTTL     time.Duration
	StepUpWindowS int
}

// defaults mirror the seeded settings; a missing row never fails a request.
const (
	defaultIdle     = 900 * time.Second
	defaultMaxUsers = 3
	defaultStepUp   = 300
)

// LoadConfig reads terminal settings and the step-up window from the settings table.
func LoadConfig(ctx context.Context, db *sql.DB) (Config, error) {
	c := Config{IdleTimeout: defaultIdle, MaxPerUser: defaultMaxUsers, TicketTTL: TicketTTL, StepUpWindowS: defaultStepUp}
	rows, err := db.QueryContext(ctx, `SELECT key,value FROM settings WHERE key IN
		('terminal.allow_root','terminal.record_sessions','terminal.idle_timeout_s','terminal.max_sessions_per_user','step_up_window_s')`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return c, err
		}
		switch k {
		case "terminal.allow_root":
			c.AllowRoot = v == "true"
		case "terminal.record_sessions":
			c.Record = v == "true"
		case "terminal.idle_timeout_s":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				c.IdleTimeout = time.Duration(n) * time.Second
			}
		case "terminal.max_sessions_per_user":
			if n, err := strconv.Atoi(v); err == nil && n >= 0 {
				c.MaxPerUser = n
			}
		case "step_up_window_s":
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				c.StepUpWindowS = n
			}
		}
	}
	return c, rows.Err()
}

// Session is one terminal_sessions row and, with JSON, the TerminalSession API object.
type Session struct {
	ID         string
	UserID     string
	UserEmail  string
	IP         string
	Target     string // "host" or "container"
	TargetName string
	AsUser     string
	IsRoot     bool
	StartedAt  string
	EndedAt    string // empty while open
	EndReason  string
	Recorded   bool
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// JSON renders the contract object: ended_at is null while the shell is open.
func (s *Session) JSON() map[string]any {
	var ended any
	if s.EndedAt != "" {
		ended = s.EndedAt
	}
	return map[string]any{
		"id": s.ID, "user_email": s.UserEmail, "ip": s.IP, "target": s.Target, "target_name": s.TargetName,
		"as_user": s.AsUser, "is_root": s.IsRoot, "started_at": s.StartedAt, "ended_at": ended,
		"recorded": s.Recorded,
	}
}

// Store persists session rows. Every method is safe to call from the shell's goroutines.
type Store struct{ DB *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{DB: db} }

const sessionCols = `id,user_id,user_email,ip,target,target_name,as_user,is_root,started_at,IFNULL(ended_at,''),end_reason,recorded`

func scanSession(row interface{ Scan(...any) error }) (Session, error) {
	var s Session
	var root, rec int
	var ended sql.NullString
	err := row.Scan(&s.ID, &s.UserID, &s.UserEmail, &s.IP, &s.Target, &s.TargetName, &s.AsUser, &root,
		&s.StartedAt, &ended, &s.EndReason, &rec)
	s.EndedAt = ended.String
	s.IsRoot = root == 1
	s.Recorded = rec == 1
	return s, err
}

// Open inserts the session row and enforces the per-user concurrency limit in the same transaction, so two
// simultaneous upgrades cannot both pass the check. max <= 0 means unlimited.
func (st *Store) Open(ctx context.Context, s *Session, max int) error {
	if s.StartedAt == "" {
		s.StartedAt = iso(time.Now())
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if max > 0 {
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM terminal_sessions WHERE user_id=? AND ended_at IS NULL`, s.UserID).Scan(&open); err != nil {
			return err
		}
		if open >= max {
			return ErrLimit
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO terminal_sessions
		(id,user_id,user_email,ip,target,target_name,as_user,is_root,started_at,end_reason,recorded)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		s.ID, s.UserID, s.UserEmail, s.IP, s.Target, s.TargetName, s.AsUser, b2i(s.IsRoot), s.StartedAt, "", b2i(s.Recorded))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Close marks a session ended. An already-closed row is left alone: the first reason (idle, exit, socket
// close) is the truthful one.
func (st *Store) Close(ctx context.Context, id, reason string) error {
	_, err := st.DB.ExecContext(ctx, `UPDATE terminal_sessions SET ended_at=?, end_reason=? WHERE id=? AND ended_at IS NULL`,
		iso(time.Now()), reason, id)
	return err
}

// List pages sessions newest first by keyset on id (ULIDs sort by creation time).
func (st *Store) List(ctx context.Context, limit int, cursor string) ([]Session, string, error) {
	q := `SELECT ` + sessionCols + ` FROM terminal_sessions`
	var args []any
	if cursor != "" {
		q += ` WHERE id<?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	rows, err := st.DB.QueryContext(ctx, q, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	items := []Session{}
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, "", err
		}
		items = append(items, s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		// The cursor is the last row on this page; the next page starts strictly below it.
		next = items[limit-1].ID
		items = items[:limit]
	}
	return items, next, nil
}

// Recover closes session rows left open by a crash so the concurrency limit cannot be permanently blocked by
// history. Called once at daemon start, before any listener accepts an upgrade.
func (st *Store) Recover(ctx context.Context) (int, error) {
	res, err := st.DB.ExecContext(ctx, `UPDATE terminal_sessions SET ended_at=?, end_reason='daemon_restart' WHERE ended_at IS NULL`,
		iso(time.Now()))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
