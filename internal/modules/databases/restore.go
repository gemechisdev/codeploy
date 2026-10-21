package databases

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/objectstore"
)

// RestoreResult is the contract's RestoreResult: the restore happened, and here is the file holding what the
// database looked like a moment before it did.
type RestoreResult struct {
	OK                 bool   `json:"ok"`
	PreRestoreSnapshot string `json:"pre_restore_snapshot"`
}

// graceSeconds is how long a container is given to shut down cleanly. Redis is the engine that cares: it writes its
// final RDB on SIGTERM, and killing it sooner is a corrupted last save.
const graceSeconds = 10

// Restore puts a verified backup back, after taking a snapshot of what is there now.
//
// The snapshot is not a courtesy and it is not skippable. A restore that made things worse is answered by "what was
// it before", and the only moment that state exists is before the load. So the snapshot is taken and verified
// first, and if it cannot be taken the restore does not start: an operator who wants a backup applied over a
// database they cannot recover from has to say so deliberately, and this panel does not offer that.
func (s *Service) Restore(ctx context.Context, runID string) (RestoreResult, error) {
	rn, err := s.GetRun(ctx, s.DB, runID)
	if err != nil {
		return RestoreResult{}, err
	}
	if rn.Status != "success" {
		return RestoreResult{}, Conflict{Msg: fmt.Sprintf("A %s backup cannot be restored", rn.Status)}
	}
	sc, err := s.GetSchedule(ctx, s.DB, rn.ScheduleID)
	if err != nil {
		return RestoreResult{}, err
	}
	r, err := s.Lookup(ctx, s.DB, sc.ResourceID)
	if err != nil {
		return RestoreResult{}, err
	}
	container, err := s.container(ctx, r)
	if err != nil {
		return RestoreResult{}, err
	}

	execCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()

	snapshot, err := s.snapshot(execCtx, sc, r)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("the current state of %s could not be captured, so nothing was restored: %w", r.Name, err)
	}
	if err := s.load(execCtx, container, r, rn); err != nil {
		return RestoreResult{PreRestoreSnapshot: snapshot}, err
	}
	return RestoreResult{OK: true, PreRestoreSnapshot: snapshot}, nil
}

// snapshot dumps the database as it stands into the pre-restore directory and returns the path it landed at.
//
// It always writes locally, whatever the schedule's destination says: a snapshot whose only copy is in a bucket is
// not something an operator standing at a broken database can use in the next five minutes, and this host is the
// one that has it.
func (s *Service) snapshot(ctx context.Context, sc Schedule, r Resource) (string, error) {
	plan, err := r.Engine.DumpPlan(ctx, r)
	if err != nil {
		return "", err
	}
	container, err := s.container(ctx, r)
	if err != nil {
		return "", err
	}
	id := s.id("bkr_")
	started := s.now()
	path := filepath.Join(s.Root(), "pre-restore", payloadName(TriggerPreRestore, id, started, plan.Ext))

	if _, err := s.DB.ExecContext(ctx, `INSERT INTO backup_runs(id,schedule_id,status,size_bytes,location,started_at,trigger) VALUES (?,?,'running',0,?,?,?)`,
		id, sc.ID, path, s.ts(started), TriggerPreRestore); err != nil {
		return "", err
	}

	size, checksum, err := s.writePayload(ctx, container, r, plan, path)
	if err != nil {
		s.failRun(ctx, id, err)
		return "", err
	}
	// The same verification a scheduled backup gets. A truncated snapshot makes a restore look recoverable when it
	// is not, and this file is the only thing between the operator and an empty database.
	diskSize, diskSum, err := verifyLocal(path)
	if err != nil {
		s.failRun(ctx, id, err)
		return "", err
	}
	if diskSize != size || diskSum != checksum {
		err := fmt.Errorf("the pre-restore snapshot at %s is %d bytes with checksum %s, not the %d bytes with checksum %s that was written",
			path, diskSize, diskSum, size, checksum)
		s.failRun(ctx, id, err)
		return "", err
	}
	finished := s.ts(s.now())
	if _, err := s.DB.ExecContext(ctx, `UPDATE backup_runs SET status='success', size_bytes=?, checksum_sha256=?, finished_at=? WHERE id=?`,
		size, checksum, finished, id); err != nil {
		return path, err
	}
	return path, nil
}

// load applies one run's payload to the container that owns the database.
func (s *Service) load(ctx context.Context, container string, r Resource, rn Run) error {
	plan, err := r.Engine.RestorePlan(ctx, r, Dump{Ext: payloadExt(rn.Location), ContainerPath: rn.Location})
	if err != nil {
		return err
	}
	if plan.CopyTo != "" {
		return s.loadFile(ctx, container, r, rn, plan)
	}
	if len(plan.Stdin) == 0 {
		return fmt.Errorf("the %s engine has no restore step", r.Engine.Name())
	}

	src, closer, err := s.payloadReader(ctx, rn)
	if err != nil {
		return err
	}
	defer closer()

	out := newTail(4096)
	code, stderr, err := s.runExec(ctx, container, plan.Stdin, plan.Env, src, out)
	if err != nil {
		return err
	}
	if code != 0 {
		// A client that dies partway through a script leaves the database half-restored, which is the state the
		// snapshot exists for, so say plainly that it happened rather than softening it.
		why := stderr
		if why == "" {
			why = out.String()
		}
		if why == "" {
			return fmt.Errorf("restoring %s failed: %s exited with code %d; the pre-restore snapshot is the way back", r.Name, tool(plan.Stdin), code)
		}
		return fmt.Errorf("restoring %s failed: %s exited with code %d: %s", r.Name, tool(plan.Stdin), code, why)
	}
	return nil
}

// loadFile puts an RDB where the server reads its data from and cycles the container, which is the only way a
// running Redis takes a snapshot handed to it from outside.
func (s *Service) loadFile(ctx context.Context, container string, r Resource, rn Run, plan Restore) error {
	src, closer, err := s.payloadReader(ctx, rn)
	if err != nil {
		return err
	}
	defer closer()

	content, err := io.ReadAll(io.LimitReader(src, s.maxBytes()+1))
	if err != nil {
		return err
	}
	if int64(len(content)) > s.maxBytes() {
		return fmt.Errorf("the payload for %s is larger than %d bytes, which this host will not hold in memory", r.Name, s.maxBytes())
	}
	dir, name := filepath.Split(filepath.ToSlash(plan.CopyTo))
	if err := s.Runtime.CopyFileIn(ctx, container, filepath.FromSlash(strings.TrimSuffix(dir, "/")), name, 0o644, content); err != nil {
		return fmt.Errorf("could not place the restored file at %s: %w", plan.CopyTo, err)
	}
	if !plan.Restart {
		return nil
	}
	if err := s.Runtime.StopContainer(ctx, container, graceSeconds); err != nil {
		return fmt.Errorf("the data file is in place but %s could not be stopped to load it: %w", r.Name, err)
	}
	if err := s.Runtime.StartContainer(ctx, container); err != nil {
		return fmt.Errorf("%s was stopped for the restore and could not be started again: %w", r.Name, err)
	}
	return s.ensureRunning(ctx, container, r)
}

// ensureRunning waits for a restarted container to report running, and says what it found if it never does.
//
// The wait is short and honest: a server that will not come up because the RDB it was handed is from a newer
// version stays down, and the panel's job is to report that rather than to call the restore finished.
func (s *Service) ensureRunning(ctx context.Context, container string, r Resource) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		states, err := s.Runtime.ListContainers(ctx, runtime.LabelManaged)
		if err != nil {
			return err
		}
		for _, st := range states {
			if st.ID == container && st.Running {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s is still down after the restore; its logs say why", r.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// payloadReader opens a run's payload as the plain bytes it was before compression, from wherever it lives.
func (s *Service) payloadReader(ctx context.Context, rn Run) (io.Reader, func(), error) {
	key, local, err := parseLocation(rn.Location)
	if err != nil {
		return nil, nil, err
	}

	var raw io.ReadCloser
	if local {
		f, err := os.Open(key)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil, Conflict{Msg: fmt.Sprintf("the backup file %s is not on this host", key)}
			}
			return nil, nil, err
		}
		raw = f
	} else {
		if s.S3 == nil {
			return nil, nil, errors.New("this backup lives in an object store this host is not configured to reach")
		}
		// The recorded size is the read bound: an object that has grown past what was verified when the run finished
		// is not the backup the panel is claiming to restore.
		name, err := s.objectKey(rn.Location)
		if err != nil {
			return nil, nil, err
		}
		body, err := s.S3.Get(ctx, name, rn.SizeBytes+1)
		if errors.Is(err, objectstore.ErrObjectTooLarge) {
			return nil, nil, fmt.Errorf("the object at %s is larger than the %d bytes recorded for this run", rn.Location, rn.SizeBytes)
		}
		if err != nil {
			return nil, nil, err
		}
		raw = io.NopCloser(bytes.NewReader(body))
	}

	if !isGzip(rn.Location) {
		return raw, func() { _ = raw.Close() }, nil
	}
	gz, err := gzip.NewReader(raw)
	if err != nil {
		_ = raw.Close()
		return nil, nil, fmt.Errorf("the payload at %s is not a readable gzip stream: %w", rn.Location, err)
	}
	// Both halves have to close: the stream and whatever it was wrapped around, or a file descriptor (or the
	// connection holding the object) stays open for the life of the daemon.
	return gz, func() { _ = gz.Close(); _ = raw.Close() }, nil
}

// payloadExt is the engine suffix a stored payload carries, which is what an engine's restore step is named after.
func payloadExt(location string) string {
	switch {
	case strings.HasSuffix(location, ".rdb"):
		return ".rdb"
	default:
		return ".sql.gz"
	}
}

func isGzip(location string) bool { return strings.HasSuffix(location, ".gz") }

func tool(cmd []string) string {
	if len(cmd) == 0 {
		return "the restore command"
	}
	return filepath.Base(cmd[0])
}
