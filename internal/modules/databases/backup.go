package databases

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/objectstore"
)

// Schedule is a backup_schedules row as the contract exposes it: the resource's name is joined at read time,
// because a schedule the panel shows without saying what it backs up is not actionable.
type Schedule struct {
	ID            string  `json:"id"`
	ResourceID    string  `json:"resource_id"`
	ResourceName  string  `json:"resource_name"`
	Cron          string  `json:"cron"`
	Destination   string  `json:"destination"`
	RetentionDays int     `json:"retention_days"`
	LastRunAt     *string `json:"last_run_at"`
	LastRunStatus *string `json:"last_run_status"`
}

// Run is a backup_runs row. Trigger and ResourceID are the panel's own bookkeeping — why the attempt happened, and
// whose database it is — and neither is in the contract's BackupRun, so both stay out of the response.
type Run struct {
	ID           string  `json:"id"`
	ScheduleID   string  `json:"schedule_id"`
	ResourceName string  `json:"resource_name"`
	Status       string  `json:"status"`
	SizeBytes    int64   `json:"size_bytes"`
	Checksum     *string `json:"checksum_sha256"`
	Location     string  `json:"location"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	Error        *string `json:"error"`

	Trigger    string `json:"-"`
	ResourceID string `json:"-"`
}

// The four reasons an attempt happens. Only the first two are reachable through the API; the restore wizard and the
// deploy engine write the other two.
const (
	TriggerSchedule   = "schedule"
	TriggerManual     = "manual"
	TriggerPreRestore = "pre_restore"
	TriggerPreDeploy  = "pre_deploy"
)

func validTrigger(t string) bool {
	return t == TriggerSchedule || t == TriggerManual || t == TriggerPreRestore || t == TriggerPreDeploy
}

// JobRun is the queue kind that performs an attempt StartRun opened. The payload is the run id and nothing else:
// the row already says which schedule, where the bytes go and when the attempt began, so a job that survives a
// daemon restart needs no more than that to finish correctly.
const JobRun = "backup.run"

// CreateScheduleInput is one POST /resources/{id}/backups body, already decoded.
type CreateScheduleInput struct {
	ResourceID    string
	Cron          string
	Destination   string
	RetentionDays int
}

// CreateSchedule validates a schedule and stores it.
//
// The field messages are the form's messages, so a client that bypasses the form is told the same thing the form
// would have said. The resource is resolved and its dump plan built before the insert, because a schedule for a
// database this panel cannot dump — an unknown engine, or one deployed without a password variable a dump could
// authenticate with — is not a schedule waiting to succeed. It is a mistake about the resource, and the operator
// should learn it now rather than at three in the morning.
func (s *Service) CreateSchedule(ctx context.Context, q Querier, in CreateScheduleInput) (Schedule, error) {
	f := FieldErrors{}
	cron := strings.TrimSpace(in.Cron)
	if err := jobs.Validate(cron); err != nil {
		f["cron"] = "Enter a 5-field cron expression such as 0 3 * * *"
	}
	if in.Destination != "local" && in.Destination != "s3" {
		f["destination"] = "Choose local or s3"
	}
	if in.RetentionDays < 1 || in.RetentionDays > 365 {
		f["retention_days"] = "Retention must be 1-365 days"
	}
	if in.Destination == "s3" && s.S3 == nil {
		f["destination"] = "This host has no object store configured, so it cannot back up to s3"
	}
	if len(f) > 0 {
		return Schedule{}, f
	}

	r, err := s.Lookup(ctx, q, in.ResourceID)
	if err != nil {
		return Schedule{}, err
	}
	if _, err := r.Engine.DumpPlan(ctx, r); err != nil {
		return Schedule{}, err
	}

	id := s.id("bks_")
	if _, err := q.ExecContext(ctx, `INSERT INTO backup_schedules(id,resource_id,cron,destination,retention_days,created_at) VALUES (?,?,?,?,?,?)`,
		id, r.ID, cron, in.Destination, in.RetentionDays, s.ts(s.now())); err != nil {
		return Schedule{}, err
	}
	return s.GetSchedule(ctx, q, id)
}

const scheduleSelect = `SELECT bk.id, bk.resource_id, res.name, bk.cron, bk.destination, bk.retention_days, bk.last_run_at, bk.last_run_status
	FROM backup_schedules bk JOIN resources res ON res.id=bk.resource_id`

// GetSchedule reads one schedule with its resource name.
func (s *Service) GetSchedule(ctx context.Context, q Querier, id string) (Schedule, error) {
	var (
		sc                 Schedule
		lastAt, lastStatus sql.NullString
	)
	err := q.QueryRowContext(ctx, scheduleSelect+` WHERE bk.id=?`, id).
		Scan(&sc.ID, &sc.ResourceID, &sc.ResourceName, &sc.Cron, &sc.Destination, &sc.RetentionDays, &lastAt, &lastStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Schedule{}, NotFound{What: "backup schedule"}
	}
	if err != nil {
		return Schedule{}, err
	}
	sc.LastRunAt, sc.LastRunStatus = nullStr(lastAt), nullStr(lastStatus)
	return sc, nil
}

// ListSchedules returns every schedule, or one resource's when an id is given.
//
// Ordering by id reads as the order the schedules were created, since ids are minted in time order.
func (s *Service) ListSchedules(ctx context.Context, q Querier, resourceID string) ([]Schedule, error) {
	where, args := ``, []any{}
	if resourceID != "" {
		where, args = ` WHERE bk.resource_id=?`, []any{resourceID}
	}
	rows, err := q.QueryContext(ctx, scheduleSelect+where+` ORDER BY bk.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Schedule, 0)
	for rows.Next() {
		var (
			sc                 Schedule
			lastAt, lastStatus sql.NullString
		)
		if err := rows.Scan(&sc.ID, &sc.ResourceID, &sc.ResourceName, &sc.Cron, &sc.Destination, &sc.RetentionDays, &lastAt, &lastStatus); err != nil {
			return nil, err
		}
		sc.LastRunAt, sc.LastRunStatus = nullStr(lastAt), nullStr(lastStatus)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// runSelect carries the two columns the contract does not show, so a caller that needs them does not have to read
// the schedule back to get them.
const runSelect = `SELECT br.id, br.schedule_id, res.name, br.status, br.size_bytes, br.checksum_sha256, br.location, br.started_at, br.finished_at, br.error, br.trigger, bk.resource_id
	FROM backup_runs br JOIN backup_schedules bk ON bk.id=br.schedule_id JOIN resources res ON res.id=bk.resource_id`

// scanner is satisfied by both *sql.Rows and *sql.Row, which is what lets one row shape be read by the list and by
// the single-run lookup without a second copy of the column list.
type scanner interface{ Scan(...any) error }

func scanRun(row scanner) (Run, error) {
	var rn Run
	var checksum, finished, runErr sql.NullString
	if err := row.Scan(&rn.ID, &rn.ScheduleID, &rn.ResourceName, &rn.Status, &rn.SizeBytes, &checksum, &rn.Location,
		&rn.StartedAt, &finished, &runErr, &rn.Trigger, &rn.ResourceID); err != nil {
		return Run{}, err
	}
	rn.Checksum, rn.FinishedAt, rn.Error = nullStr(checksum), nullStr(finished), nullStr(runErr)
	return rn, nil
}

// GetRun reads one run.
func (s *Service) GetRun(ctx context.Context, q Querier, id string) (Run, error) {
	rn, err := scanRun(q.QueryRowContext(ctx, runSelect+` WHERE br.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, NotFound{What: "backup run"}
	}
	return rn, err
}

// ListRuns returns the newest runs across every schedule, newest first, with the cursor being the last id read.
//
// Ordering is on the ULID rather than on started_at: ids are minted in time order, so the page is one index seek
// over backup_runs(id DESC) plus the rows it returns, and a resumed page cannot repeat or skip a run that started
// while the previous page was being read.
func (s *Service) ListRuns(ctx context.Context, q Querier, limit int, cursor string) ([]Run, string, error) {
	if limit <= 0 {
		limit = 50
	}
	where, args := ``, []any{}
	if cursor != "" {
		where, args = ` WHERE br.id<?`, []any{cursor}
	}
	rows, err := q.QueryContext(ctx, runSelect+where+` ORDER BY br.id DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := make([]Run, 0)
	for rows.Next() {
		rn, err := scanRun(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, rn)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		next, out = out[limit-1].ID, out[:limit]
	}
	return out, next, nil
}

// StartRun opens an attempt and returns the row the panel answers with.
//
// The location is chosen before a byte is dumped, because the contract's BackupRun.location is required and a run
// that failed before it wrote anything still has to say where its bytes would have gone. A second concurrent
// attempt on the same schedule is refused rather than queued: two mysqldumps of the same database on a small host
// is a memory incident, not a backup.
func (s *Service) StartRun(ctx context.Context, q Querier, scheduleID, trigger string) (Run, error) {
	if !validTrigger(trigger) {
		return Run{}, fmt.Errorf("databases: backup trigger %q is not one the panel records", trigger)
	}
	sc, err := s.GetSchedule(ctx, q, scheduleID)
	if err != nil {
		return Run{}, err
	}
	var busy int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE schedule_id=? AND status='running'`, scheduleID).Scan(&busy); err != nil {
		return Run{}, err
	}
	if busy > 0 {
		return Run{}, Conflict{Msg: fmt.Sprintf("A backup of %s is already running", sc.ResourceName)}
	}

	r, err := s.Lookup(ctx, q, sc.ResourceID)
	if err != nil {
		return Run{}, err
	}
	plan, err := r.Engine.DumpPlan(ctx, r)
	if err != nil {
		return Run{}, err
	}
	id := s.id("bkr_")
	started := s.now()
	loc, err := s.location(sc, r, plan.Ext, trigger, id, started)
	if err != nil {
		return Run{}, err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO backup_runs(id,schedule_id,status,size_bytes,location,started_at,trigger) VALUES (?,?,'running',0,?,?,?)`,
		id, sc.ID, loc, s.ts(started), trigger); err != nil {
		return Run{}, err
	}
	if _, err := q.ExecContext(ctx, `UPDATE backup_schedules SET last_run_status='running' WHERE id=?`, sc.ID); err != nil {
		return Run{}, err
	}
	return Run{ID: id, ScheduleID: sc.ID, ResourceName: sc.ResourceName, Status: "running", Location: loc,
		StartedAt: s.ts(started), Trigger: trigger, ResourceID: sc.ResourceID}, nil
}

// Execute performs an attempt that StartRun opened: dump, verify, publish, prune.
//
// It returns nil once the run row has a final status, including when the backup itself failed. The queue retries a
// job that returns an error, and retrying is right for a bookkeeping failure and wrong for a dump: backup_runs is
// the operator's record of what happened, and a second attempt against a database that was down for the first one
// would read as two backups when there was one.
func (s *Service) Execute(ctx context.Context, runID string) error {
	rn, err := s.GetRun(ctx, s.DB, runID)
	if err != nil {
		return err
	}
	if rn.Status != "running" {
		return nil
	}
	sc, err := s.GetSchedule(ctx, s.DB, rn.ScheduleID)
	if err != nil {
		s.failRun(ctx, rn.ID, err)
		return nil
	}
	if _, err := s.dump(ctx, sc, rn); err != nil {
		s.failRun(ctx, rn.ID, err)
		return nil
	}
	if n, err := s.PruneSchedule(ctx, s.DB, sc); err != nil {
		slog.Warn("backup retention sweep failed", "schedule", sc.ID, "err", err)
	} else if n > 0 {
		slog.Info("pruned expired backups", "schedule", sc.ID, "runs", n)
	}
	return nil
}

// dump produces the payload and marks the run successful, in that order and never the other way round.
func (s *Service) dump(ctx context.Context, sc Schedule, rn Run) (Run, error) {
	r, err := s.Lookup(ctx, s.DB, sc.ResourceID)
	if err != nil {
		return rn, err
	}
	plan, err := r.Engine.DumpPlan(ctx, r)
	if err != nil {
		return rn, err
	}
	container, err := s.container(ctx, r)
	if err != nil {
		return rn, err
	}
	started, err := s.parseTS(rn.StartedAt)
	if err != nil {
		return rn, err
	}
	path := s.localPath(r, plan.Ext, rn.Trigger, rn.ID, started)

	execCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	size, checksum, err := s.writePayload(execCtx, container, r, plan, path)
	if err != nil {
		return rn, err
	}
	// Verification is a second read of what the first pass wrote, after the file is closed and renamed into place.
	// A dump truncated by a full disk has the intended size in memory and not on disk, and marking that successful
	// is the failure this phase exists to prevent.
	diskSize, diskSum, err := verifyLocal(path)
	if err != nil {
		return rn, err
	}
	if diskSize != size || diskSum != checksum {
		return rn, fmt.Errorf("the dump at %s is %d bytes with checksum %s, which is not what was written (%d bytes, %s)", path, diskSize, diskSum, size, checksum)
	}

	if sc.Destination == "s3" {
		if err := s.upload(execCtx, rn.Location, path); err != nil {
			return rn, err
		}
		if err := s.verifyRemote(execCtx, rn.Location, size); err != nil {
			return rn, err
		}
		// The bucket holds it now; a second copy on the host's only disk is a leak, not a backup.
		if err := os.Remove(path); err != nil {
			slog.Warn("could not remove the staged dump", "path", path, "err", err)
		}
	}

	finished := s.ts(s.now())
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET status='success', size_bytes=?, checksum_sha256=?, finished_at=? WHERE id=?`,
		size, checksum, finished, rn.ID); err != nil {
		return rn, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup_schedules SET last_run_status='success', last_run_at=? WHERE id=?`, finished, sc.ID); err != nil {
		return rn, err
	}
	rn.Status, rn.SizeBytes, rn.FinishedAt = "success", size, &finished
	rn.Checksum = &checksum
	return rn, nil
}

// failRun closes an attempt with the reason it failed. The run row is written with the database handle rather than
// the caller's, because the attempt has already run and its outcome must be recorded even when the job that
// recorded it is the thing that errored.
func (s *Service) failRun(ctx context.Context, runID string, cause error) {
	msg := cause.Error()
	finished := s.ts(s.now())
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET status='failed', error=?, finished_at=? WHERE id=? AND status='running'`,
		msg, finished, runID); err != nil {
		slog.Error("could not record a failed backup", "run", runID, "err", err)
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup_schedules SET last_run_status='failed', last_run_at=? WHERE id=(SELECT schedule_id FROM backup_runs WHERE id=?)`,
		finished, runID); err != nil {
		slog.Error("could not record a failed backup on its schedule", "run", runID, "err", err)
	}
	slog.Warn("backup failed", "run", runID, "err", cause)
}

// writePayload runs the dump and leaves it at path, returning the size and checksum of the bytes that landed
// there. The temp file is renamed only after a successful sync, so a crash mid-dump cannot leave a payload that
// looks complete at the final path.
func (s *Service) writePayload(ctx context.Context, container string, r Resource, plan Dump, path string) (int64, string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return 0, "", err
	}
	tmp := path + ".part"
	defer func() { _ = os.Remove(tmp) }()

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, "", err
	}
	hasher := sha256.New()
	var written int64

	if plan.Stdout {
		// The text engines stream SQL on stdout and are compressed here rather than by a shell pipeline in the
		// container, because the only way to pipe a dump through gzip in one argv array is to build a shell string
		// out of operator-supplied names.
		base := io.MultiWriter(f, hasher)
		var sink io.Writer = base
		var gz *gzip.Writer
		if strings.HasSuffix(plan.Ext, ".gz") {
			gz = gzip.NewWriter(base)
			sink = gz
		}
		code, stderr, err := s.runExec(ctx, container, plan.Cmd, plan.Env, nil, sink)
		if gz != nil {
			// Closed before the size is taken: an unflushed tail is a payload that only fails when someone tries to
			// restore it.
			if cerr := gz.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			_ = f.Close()
			return 0, "", err
		}
		if code != 0 {
			_ = f.Close()
			return 0, "", dumpFailed(plan, code, stderr)
		}
		if written, err = f.Seek(0, io.SeekCurrent); err != nil {
			_ = f.Close()
			return 0, "", err
		}
	} else {
		// A file engine writes its payload inside the container and the archive port hands the bytes over in memory,
		// so the size bound is enforced here rather than trusted from the container.
		code, stderr, err := s.runExec(ctx, container, plan.Cmd, plan.Env, nil, io.Discard)
		if err != nil {
			_ = f.Close()
			return 0, "", err
		}
		if code != 0 {
			_ = f.Close()
			return 0, "", dumpFailed(plan, code, stderr)
		}
		content, err := s.Runtime.CopyFileOut(ctx, container, plan.ContainerPath, s.maxBytes())
		if err != nil {
			_ = f.Close()
			return 0, "", fmt.Errorf("could not read the dump out of %s: %w", r.Name, err)
		}
		if _, err := f.Write(content); err != nil {
			_ = f.Close()
			return 0, "", err
		}
		hasher.Write(content)
		written = int64(len(content))
	}

	if written == 0 {
		_ = f.Close()
		return 0, "", fmt.Errorf("%s produced an empty dump", r.Name)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return 0, "", err
	}
	if err := f.Close(); err != nil {
		return 0, "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return 0, "", err
	}
	return written, hex.EncodeToString(hasher.Sum(nil)), nil
}

// closeWriter ends a gzip stream, if the sink still has one open. A truncated gzip member is what an unflushed
// writer leaves behind, and it is only discovered on the restore.
func closeWriter(w io.Writer) error {
	if gz, ok := w.(*gzip.Writer); ok {
		return gz.Close()
	}
	return nil
}

func dumpFailed(plan Dump, code int, stderr string) error {
	if stderr == "" {
		return fmt.Errorf("%s exited with code %d", tool(plan.Cmd), code)
	}
	return fmt.Errorf("%s exited with code %d: %s", tool(plan.Cmd), code, stderr)
}

// upload puts a staged payload in the bucket under the key the run's location names.
func (s *Service) upload(ctx context.Context, location, path string) error {
	if s.S3 == nil {
		return errors.New("this host has no object store configured")
	}
	key, err := s.objectKey(location)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	// The size and digest are taken from the file about to be sent, not carried over from the dump: Put refuses a
	// stream that does not match them, and what is being uploaded is the file rather than the intention.
	sum, err := fileChecksum(path)
	if err != nil {
		return err
	}
	return s.S3.Put(ctx, key, f, st.Size(), sum)
}

// verifyRemote asks the store what it holds and compares that with what was written.
//
// The digest is not read back from the object. The upload was signed with it, so the store has already refused the
// write if the bytes did not match; re-fetching a dump to prove a hash the store verified on receipt costs the
// bandwidth of a second upload for information we already have.
func (s *Service) verifyRemote(ctx context.Context, location string, size int64) error {
	if s.S3 == nil {
		return errors.New("this host has no object store configured")
	}
	key, err := s.objectKey(location)
	if err != nil {
		return err
	}
	got, _, err := s.S3.Stat(ctx, key)
	if err != nil {
		return fmt.Errorf("the object store would not confirm the upload: %w", err)
	}
	if got != size {
		return fmt.Errorf("the object store holds %d bytes at %s, the dump was %d bytes", got, key, size)
	}
	return nil
}

// PruneSchedule removes a schedule's finished runs older than its retention, with their payloads.
//
// An attempt that failed is aged out on the same window as one that succeeded: it has no payload to lose, and a
// history that grows without bound is a leak even when the rows are small. A row still running is left alone,
// because the attempt may be live and its record is the only thing telling the operator so.
//
// A row is deleted only after its payload is gone, so the history never advertises a backup that cannot be
// restored. A payload that is already missing is not an error: a sweep is allowed to catch up on its own records.
func (s *Service) PruneSchedule(ctx context.Context, q Querier, sc Schedule) (int, error) {
	if sc.RetentionDays <= 0 {
		return 0, nil
	}
	cutoff := s.ts(s.now().Add(-time.Duration(sc.RetentionDays) * 24 * time.Hour))
	rows, err := q.QueryContext(ctx, `SELECT id, location FROM backup_runs WHERE schedule_id=? AND status<>'running' AND started_at<? ORDER BY id`, sc.ID, cutoff)
	if err != nil {
		return 0, err
	}
	type expiring struct{ id, location string }
	var victims []expiring
	for rows.Next() {
		var e expiring
		if err := rows.Scan(&e.id, &e.location); err != nil {
			rows.Close()
			return 0, err
		}
		victims = append(victims, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}

	for _, e := range victims {
		if err := s.removePayload(ctx, e.location); err != nil {
			return 0, fmt.Errorf("could not delete the expired backup %s: %w", e.id, err)
		}
		if _, err := q.ExecContext(ctx, `DELETE FROM backup_runs WHERE id=?`, e.id); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

// removePayload deletes the bytes a run pointed at, wherever they live.
func (s *Service) removePayload(ctx context.Context, location string) error {
	key, local, err := parseLocation(location)
	if err != nil {
		return err
	}
	if local {
		if err := os.Remove(key); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if s.S3 == nil {
		// No client on this host can reach the bucket. The object is not orphaned silently: the bucket's own
		// lifecycle rule is the other half of retention for an s3 destination.
		slog.Warn("cannot delete an s3 backup without an object store configured", "location", location)
		return nil
	}
	name, err := s.objectKey(location)
	if err != nil {
		return err
	}
	if err := s.S3.Delete(ctx, name); err != nil && !errors.Is(err, objectstore.ErrNotFound) {
		return err
	}
	return nil
}

// location is where a run's payload ends up: an s3 URI when the schedule writes to the bucket, a host path
// otherwise. Both are derived from the run id and its start time, so any point in an attempt's life can rebuild the
// answer instead of having to remember it.
func (s *Service) location(sc Schedule, r Resource, ext, trigger, runID string, started time.Time) (string, error) {
	if sc.Destination != "s3" {
		return s.localPath(r, ext, trigger, runID, started), nil
	}
	if s.S3 == nil {
		return "", errors.New("this host has no object store configured")
	}
	name := payloadName(trigger, runID, started, ext)
	// The bucket is organised the same way the host directory is: one segment per resource, so a listing of a
	// shared bucket reads as the panel's history does.
	return "s3://" + s.S3.Bucket() + "/" + s.S3.Key(safeSegment(r.Slug)+"/"+name), nil
}

// localPath is the host path a payload is staged at, whether or not that is where it stays.
func (s *Service) localPath(r Resource, ext, trigger, runID string, started time.Time) string {
	return filepath.Join(s.Root(), safeSegment(r.Slug), payloadName(trigger, runID, started, ext))
}

// payloadName is the file name: the instant the attempt started, why it started, and eight characters of the run
// id. Two runs on one day are told apart by the id, and the reason is in the name because an operator reading a
// directory listing is not going to query the database first.
func payloadName(trigger, runID string, started time.Time, ext string) string {
	if ext == "" {
		ext = ".sql.gz"
	}
	if trigger == "" {
		trigger = TriggerSchedule
	}
	return fmt.Sprintf("%s-%s-%s%s", started.UTC().Format("2006-01-02T1504Z"), strings.ReplaceAll(trigger, "_", "-"), shortID(runID, 8), ext)
}

// parseLocation splits a recorded location into its key and whether it names a host path.
func parseLocation(location string) (string, bool, error) {
	if rest := strings.TrimPrefix(location, "s3://"); rest != location {
		i := strings.Index(rest, "/")
		if i <= 0 || i == len(rest)-1 {
			return "", false, fmt.Errorf("malformed object location %q", location)
		}
		return rest[i+1:], false, nil
	}
	if !filepath.IsAbs(location) {
		return "", false, fmt.Errorf("malformed backup location %q", location)
	}
	return location, true, nil
}

// objectKey turns a recorded s3 location into the name the object store client expects.
//
// The location holds the full key, prefix included, because that is what an operator types into a bucket listing;
// Put, Get, Stat and Delete apply the prefix again themselves, so the stored form has to be reduced back to the
// name before it is used. A location outside the configured prefix is refused rather than silently re-prefixed:
// it means the schedule was written against a different bucket layout, and restoring that quietly is how a panel
// ends up reading someone else's object.
func (s *Service) objectKey(location string) (string, error) {
	if s.S3 == nil {
		return "", errors.New("this host has no object store configured")
	}
	key, local, err := parseLocation(location)
	if err != nil {
		return "", err
	}
	if local {
		return "", fmt.Errorf("%s is a host path, not an object location", location)
	}
	if p := s.S3.Prefix(); p != "" {
		if !strings.HasPrefix(key, p+"/") {
			return "", fmt.Errorf("the object %s is outside this host's configured prefix %s", key, p)
		}
		return strings.TrimPrefix(key, p+"/"), nil
	}
	return key, nil
}

// verifyLocal re-reads a file and reports the size and checksum it actually holds.
func verifyLocal(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, "", err
	}
	return st.Size(), hex.EncodeToString(h.Sum(nil)), nil
}

func fileChecksum(path string) (string, error) {
	_, sum, err := verifyLocal(path)
	return sum, err
}

// safeSegment makes a string usable as one path segment. The slug is policy-validated by the time it is in the row,
// and this is still not the place to trust that: a resource name that arrived some other way must not be able to
// write outside the backups directory.
func safeSegment(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "resource"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "resource"
	}
	return out
}

func shortID(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
