package deploy

import (
	"context"
	"database/sql"
	"sync"
	"time"
)

const (
	maxLineLen    = 4096
	defaultMaxLog = 20000
)

// logWriter appends redacted, size-capped lines to deployment_logs. The cap is the "log writers are size-capped"
// control: once it is hit one marker line is written and the rest is dropped, so a runaway build cannot fill the disk
// through the database either.
type logWriter struct {
	db     *sql.DB
	id     string
	red    *Redactor
	max    int
	pub    func(string, any)
	now    func() time.Time
	mu     sync.Mutex
	seq    int
	capHit bool
}

func newLogWriter(db *sql.DB, id string, red *Redactor, max int, pub func(string, any), now func() time.Time) *logWriter {
	if max <= 0 {
		max = defaultMaxLog
	}
	w := &logWriter{db: db, id: id, red: red, max: max, pub: pub, now: now}
	_ = db.QueryRow(`SELECT COALESCE(MAX(seq),0) FROM deployment_logs WHERE deployment_id=?`, id).Scan(&w.seq)
	return w
}

// Line writes one line. stream is stdout, stderr or system. Errors writing logs never fail a deployment.
func (w *logWriter) Line(stream, line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.capHit {
		return
	}
	if w.seq >= w.max {
		w.capHit = true
		w.write("system", "log output truncated: the per-deployment limit was reached")
		return
	}
	line = w.red.Redact(line)
	if len(line) > maxLineLen {
		line = line[:maxLineLen] + " ...[truncated]"
	}
	w.write(stream, line)
}

func (w *logWriter) write(stream, line string) {
	w.seq++
	_, err := w.db.ExecContext(context.Background(), `INSERT INTO deployment_logs(deployment_id,seq,at,stream,line) VALUES (?,?,?,?,?)`,
		w.id, w.seq, w.now().UTC().Format("2006-01-02T15:04:05.000Z"), stream, line)
	if err == nil && w.pub != nil {
		w.pub("deployment.log", map[string]any{"deployment_id": w.id, "seq": w.seq})
	}
}

// Sys writes an engine-authored line.
func (w *logWriter) Sys(line string) { w.Line("system", line) }
