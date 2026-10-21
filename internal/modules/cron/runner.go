// Package cron runs the scheduled tasks an operator attaches to a resource (plan K2): a 5-field expression, an
// argv array executed inside that resource's own container, and a bounded execution history.
//
// Three rules the package is built around.
//
// A job is an argument list, never a shell string. The command is stored as a JSON argv array and handed to the
// runtime as argv, so nothing the panel writes is ever parsed by a shell — which matters because the argv arrives
// from a CLI argument an operator typed.
//
// The container is resolved the way a backup resolves one: the active deployment's id first, then the managed
// resource label. A job whose container is gone is recorded as skipped rather than dropped, so "my cron never ran"
// has an answer in the history.
//
// Output is redacted and bounded. cron_job_runs is a plaintext table, and a maintenance command run inside a
// container sees that container's secrets in its environment, so the tail that gets stored has every secret value
// the panel knows about masked out of it before it is written.
package cron

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/runtime"
)

// maxOutput is how much of a job's combined output the history keeps: the tail, because what explains a failure is
// the end of it. Long enough to diagnose, short enough that a job on a one-minute schedule cannot fill the
// database. (8 KiB x 50 rows per job x a few hundred jobs is single-digit megabytes.)
const maxOutput = 8192

// Execer is the slice of the container port this package calls.
type Execer interface {
	ListContainers(ctx context.Context, labelKey string) ([]runtime.State, error)
	ExecWithEnv(ctx context.Context, container string, cmd, env []string) (runtime.ExecStream, error)
}

// Service is the package's entry point. Runtime is nil on a host without Docker, and every job then records itself
// as skipped rather than the loop failing.
type Service struct {
	DB      *sql.DB
	Key     []byte
	Runtime Execer
	Now     func() time.Time
	// mu guards running, the set of jobs with an attempt in flight.
	mu      sync.Mutex
	running map[string]bool
}

func New(db *sql.DB, key []byte) *Service {
	return &Service{DB: db, Key: key, running: map[string]bool{}}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) id(prefix string) string { return crypto.ULID(prefix, s.now().UnixMilli()) }

// FieldErrors, NotFound and Conflict are the module's failure shapes, as in the other P6 packages.
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return "validation failed" }

type NotFound struct{ What string }

func (e NotFound) Error() string { return e.What + " not found" }

type Conflict struct{ Msg string }

func (e Conflict) Error() string { return e.Msg }

// Job is one resource_cron_jobs row in its useful form: the argv decoded, the flags typed.
type Job struct {
	ID         string   `json:"id"`
	ResourceID string   `json:"resource_id"`
	Name       string   `json:"name"`
	Cron       string   `json:"cron"`
	Command    []string `json:"command"`
	TimeoutS   int      `json:"timeout_s"`
	Enabled    bool     `json:"enabled"`
	LastRunAt  *string  `json:"last_run_at"`
	LastStatus *string  `json:"last_status"`
}

// Run is one cron_job_runs row: what happened, how long it took and the tail of what it printed.
type Run struct {
	ID         string `json:"id"`
	JobID      string `json:"job_id"`
	At         string `json:"at"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Status     string `json:"status"`
	Output     string `json:"output"`
	Error      string `json:"error"`
}

// AddInput is one `codeplo cron add`.
type AddInput struct {
	ResourceNameOrID string
	Name             string
	Cron             string
	Command          []string
	TimeoutS         int
}

// List returns every job with its resource's name, so a terminal listing says what a job belongs to.
func (s *Service) List(ctx context.Context) ([]Job, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT j.id, j.resource_id, r.name, j.name, j.cron, j.command_json, j.timeout_s, j.enabled, j.last_run_at, j.last_status
		FROM resource_cron_jobs j JOIN resources r ON r.id=j.resource_id ORDER BY j.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var (
			j        Job
			cmd, raw string
			yes      int
			lastAt   sql.NullString
			lastStat sql.NullString
		)
		if err := rows.Scan(&j.ID, &j.ResourceID, &raw, &j.Name, &j.Cron, &cmd, &j.TimeoutS, &yes, &lastAt, &lastStat); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(cmd), &j.Command); err != nil {
			return nil, fmt.Errorf("cron job %s has an unreadable command: %w", j.ID, err)
		}
		j.Enabled = yes != 0
		j.LastRunAt, j.LastStatus = strValue(lastAt), strValue(lastStat)
		out = append(out, j)
	}
	return out, rows.Err()
}

// Add validates and stores a job. The cron expression and the argv are both checked here rather than at run time,
// because a job that cannot parse is a job that will silently never run, and the operator is standing at a terminal.
func (s *Service) Add(ctx context.Context, in AddInput) (Job, error) {
	f := FieldErrors{}
	target := strings.TrimSpace(in.ResourceNameOrID)
	var id, name string
	if err := s.DB.QueryRowContext(ctx, `SELECT id, name FROM resources WHERE id=? OR name=?`, target, target).Scan(&id, &name); errors.Is(err, sql.ErrNoRows) {
		f["resource"] = fmt.Sprintf("no resource named %q", target)
	} else if err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(in.Name) == "" {
		f["name"] = "Give the job a name"
	}
	if err := jobs.Validate(in.Cron); err != nil {
		f["cron"] = "Enter a 5-field cron expression such as */5 * * * *"
	}
	if len(in.Command) == 0 || strings.TrimSpace(in.Command[0]) == "" {
		f["command"] = "Give the command to run"
	}
	for i, c := range in.Command {
		if strings.IndexByte(c, 0) >= 0 {
			f["command"] = fmt.Sprintf("argument %d contains a NUL byte", i)
		}
	}
	timeout := in.TimeoutS
	if timeout == 0 {
		timeout = 600
	}
	if timeout < 10 || timeout > 3600 {
		f["timeout_s"] = "Timeout must be 10-3600 seconds"
	}
	if len(f) > 0 {
		return Job{}, f
	}

	j := Job{ID: s.id("crn_"), ResourceID: id, Name: strings.TrimSpace(in.Name), Cron: strings.TrimSpace(in.Cron),
		Command: in.Command, TimeoutS: timeout, Enabled: true}
	raw, err := json.Marshal(j.Command)
	if err != nil {
		return Job{}, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO resource_cron_jobs(id,resource_id,name,cron,command_json,timeout_s,enabled,created_at) VALUES (?,?,?,?,?,?,1,?)`,
		j.ID, j.ResourceID, j.Name, j.Cron, string(raw), j.TimeoutS, s.ts(s.now()))
	if err != nil {
		return Job{}, err
	}
	return j, nil
}

// Remove deletes a job and, with it, its history (the FK is ON DELETE CASCADE).
func (s *Service) Remove(ctx context.Context, id string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM resource_cron_jobs WHERE id=? OR name=?`, id, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return NotFound{What: "cron job"}
	}
	if n > 1 {
		// Two jobs with the same name: both were deleted, and saying so is better than having looked like a
		// single-job command while removing a second operator's task.
		return fmt.Errorf("removed %d jobs that matched %q", n, id)
	}
	return nil
}

// History returns a job's attempts, newest first. The job is addressed by id or by name, which is how the operator
// named it when adding it.
func (s *Service) History(ctx context.Context, idOrName string, limit int) ([]Run, error) {
	var id string
	if err := s.DB.QueryRowContext(ctx, `SELECT id FROM resource_cron_jobs WHERE id=? OR name=? LIMIT 1`, idOrName, idOrName).Scan(&id); errors.Is(err, sql.ErrNoRows) {
		return nil, NotFound{What: "cron job"}
	} else if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id,job_id,at,exit_code,duration_ms,status,output,error FROM cron_job_runs WHERE job_id=? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.JobID, &r.At, &r.ExitCode, &r.DurationMS, &r.Status, &r.Output, &r.Error); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Tick runs every job whose expression matches the current minute and returns how many attempts were made.
//
// A job already in flight is never started again: a task on a one-minute schedule with a longer timeout would
// otherwise stack a new exec against the container every minute. The attempt is recorded as skipped rather than
// swallowed, so the history answers "why did it not run".
func (s *Service) Tick(ctx context.Context) (int, error) {
	minute := s.now().Truncate(time.Minute)
	list, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	ran := 0
	for _, j := range list {
		if !j.Enabled {
			continue
		}
		sched, err := jobs.Parse(j.Cron)
		if err != nil {
			slog.Warn("cron job has an expression the panel cannot parse", "job", j.ID, "cron", j.Cron, "err", err)
			continue
		}
		if !sched.Matches(minute) {
			continue
		}
		// Idempotence for the minute: a run row at or after its opening means this slot was already taken, which
		// is what makes a restarted daemon's first tick safe against the minute it missed.
		var seen int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM cron_job_runs WHERE job_id=? AND at>=?`, j.ID, s.ts(minute)).Scan(&seen); err != nil {
			return ran, err
		}
		if seen > 0 {
			continue
		}
		if !s.start(j.ID) {
			if err := s.record(ctx, j, Run{Status: "skipped", ExitCode: -1,
				Error: "the previous run is still going"}); err != nil {
				return ran, err
			}
			ran++
			continue
		}
		if err := s.run(ctx, j, minute); err != nil {
			s.finish(j.ID)
			return ran, err
		}
		s.finish(j.ID)
		ran++
	}
	return ran, nil
}

// start claims a job for this process, and reports false when an attempt is already in flight.
func (s *Service) start(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == nil {
		s.running = map[string]bool{}
	}
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) finish(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.running, id)
}

// run performs one attempt: resolve the container, exec the argv with the resource's own environment, and record
// what came back. Every path writes a row, including the ones that never reached the container.
func (s *Service) run(ctx context.Context, j Job, at time.Time) error {
	container, err := s.container(ctx, j)
	if err != nil {
		return s.record(ctx, j, Run{Status: "skipped", ExitCode: -1, Error: err.Error()})
	}
	env, red, err := s.env(ctx, j.ResourceID)
	if err != nil {
		return s.record(ctx, j, Run{Status: "failed", ExitCode: -1, Error: err.Error()})
	}
	deadline := time.Duration(j.TimeoutS) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	started := s.now()
	out, code, runErr := s.exec(runCtx, container, j.Command, env)
	elapsed := s.now().Sub(started)
	rn := Run{At: s.ts(at), ExitCode: code, DurationMS: elapsed.Milliseconds(), Output: tail(red.Redact(out), maxOutput)}
	switch {
	case errors.Is(runErr, context.DeadlineExceeded):
		rn.Status, rn.Error = "failed", fmt.Sprintf("timed out after %s", deadline)
	case runErr != nil:
		rn.Status, rn.Error = "failed", runErr.Error()
	case code != 0:
		rn.Status, rn.Error = "failed", fmt.Sprintf("exited %d", code)
	default:
		rn.Status = "success"
	}
	return s.record(ctx, j, rn)
}

// exec runs the argv and returns its combined output plus the exit code. The framed stdout/stderr is demultiplexed
// into one buffer: a job's error lines belong next to its output lines in the history, not in a second column.
func (s *Service) exec(ctx context.Context, container string, cmd, env []string) (string, int, error) {
	if s.Runtime == nil {
		return "", -1, errors.New("no container runtime on this host")
	}
	stream, err := s.Runtime.ExecWithEnv(ctx, container, cmd, env)
	if err != nil {
		return "", -1, err
	}
	defer stream.Close()
	done := make(chan struct{})
	defer close(done)
	// Nothing in the runtime port aborts a hijacked attach when the context expires, so a job that overruns its
	// timeout would otherwise hold the connection (and its container process) open past the tick.
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()
	buf := &limited{buf: make([]byte, 0, 4096), max: maxOutput * 4}
	// stdout and stderr land in the same buffer: in a history row the lines of one failure belong in the order the
	// process wrote them, not in two columns a reader has to interleave by hand.
	if _, err := runtime.CopyDemuxed(stream.Stdout(), buf, buf); err != nil {
		return buf.String(), -1, err
	}
	code, err := stream.Wait(ctx)
	if err != nil {
		return buf.String(), code, err
	}
	return buf.String(), code, nil
}

// container is where the job runs: the active deployment's container, then any running managed container for the
// resource. A release-role container is not it — that job has already exited by the time a schedule could fire.
func (s *Service) container(ctx context.Context, j Job) (string, error) {
	if s.Runtime == nil {
		return "", errors.New("no container runtime on this host")
	}
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT d.container_id FROM deployments d JOIN resources res ON res.active_deployment_id=d.id WHERE res.id=?`, j.ResourceID).Scan(&id)
	if err == nil && strings.TrimSpace(id) != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	states, err := s.Runtime.ListContainers(ctx, runtime.LabelResource)
	if err != nil {
		return "", err
	}
	for _, st := range states {
		if st.Labels[runtime.LabelResource] == j.ResourceID && st.Running && st.Labels[runtime.LabelRole] != runtime.RoleRelease {
			return st.ID, nil
		}
	}
	return "", fmt.Errorf("resource %s has no running container", j.Name)
}

// env is the resource's own runtime environment, which is what a maintenance command inside its container needs to
// reach its own database, plus the redactor that keeps those values out of the stored output.
func (s *Service) env(ctx context.Context, resourceID string) ([]string, *deploy.Redactor, error) {
	vars, err := deploy.LoadEnv(ctx, s.DB, s.Key, resourceID)
	if err != nil {
		return nil, nil, err
	}
	env := make([]string, 0, len(vars))
	secrets := []string{}
	for _, v := range vars {
		if !v.Runtime() {
			continue
		}
		env = append(env, v.Key+"="+v.Value)
		secrets = append(secrets, v.Value)
	}
	return env, deploy.NewRedactor(secrets), nil
}

// record writes the attempt, updates the job's last-run columns and sweeps the history to the configured depth.
func (s *Service) record(ctx context.Context, j Job, rn Run) error {
	if rn.At == "" {
		rn.At = s.ts(s.now())
	}
	rn.ID, rn.JobID = s.id("cjr_"), j.ID
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO cron_job_runs(id,job_id,at,exit_code,duration_ms,status,output,error) VALUES (?,?,?,?,?,?,?,?)`,
		rn.ID, rn.JobID, rn.At, rn.ExitCode, rn.DurationMS, rn.Status, rn.Output, rn.Error); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE resource_cron_jobs SET last_run_at=?, last_status=? WHERE id=?`, rn.At, rn.Status, j.ID); err != nil {
		return err
	}
	keep := settingInt(ctx, s.DB, "cron.history_per_job", 50)
	// Ids are ULIDs, so "the newest keep" is an ORDER BY id DESC LIMIT — no timestamp parsing, and the sweep uses
	// the same index the history read does.
	_, err := s.DB.ExecContext(ctx, `DELETE FROM cron_job_runs WHERE job_id=? AND id NOT IN (
		SELECT id FROM cron_job_runs WHERE job_id=? ORDER BY id DESC LIMIT ?)`, j.ID, j.ID, keep)
	return err
}

func strValue(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// settingInt reads a numeric setting, falling back when the row is absent or holds something that is not a positive
// number. Like the other P6 modules this package carries its own copy rather than importing a shared one: the whole
// dependency is the settings table, and a common helper would mean a package every module has to know about.
func settingInt(ctx context.Context, db *sql.DB, key string, def int) int {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// tail keeps the last n bytes of s on a byte boundary that is not inside a UTF-8 sequence's middle.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	for len(s) > 0 && (s[0]&0xC0) == 0x80 {
		s = s[1:]
	}
	return s
}

// limited is a writer that stops collecting at max without failing: a runaway job is truncated in the history,
// not an error that hides the exit code the operator cares about.
type limited struct {
	buf []byte
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if len(l.buf) < l.max {
		room := l.max - len(l.buf)
		if len(p) > room {
			l.buf = append(l.buf, p[:room]...)
		} else {
			l.buf = append(l.buf, p...)
		}
	}
	return len(p), nil
}

func (l *limited) String() string { return string(l.buf) }
