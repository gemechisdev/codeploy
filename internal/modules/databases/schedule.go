package databases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/store"
)

// ScheduleTick fires every schedule whose cron expression matches the current minute, and queues the dump it
// starts. It is called by the daemon once a minute, and it is written to be safe to call twice in a minute.
//
// The due test is `Matches(thisMinute)` rather than "the next occurrence after the last run", which is what a
// restarted daemon would need a saved next-fire column for. The guard against a double fire is the run row: an
// attempt that started at or after this minute's opening means this slot has already been taken. A minute the
// daemon was not running for is missed rather than caught up — the same thing Vixie cron does on a machine that
// was asleep, and the honest behaviour when a missed backup and a duplicated one are both possible answers.
//
// The run is opened and enqueued in one transaction, so a schedule that fired cannot exist without a job that
// will finish it, and a job cannot exist for a run nobody recorded.
func (s *Service) ScheduleTick(ctx context.Context, q *jobs.Queue) (int, error) {
	type slot struct {
		id, cron string
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, cron FROM backup_schedules ORDER BY id`)
	if err != nil {
		return 0, err
	}
	var all []slot
	for rows.Next() {
		var sc slot
		if err := rows.Scan(&sc.id, &sc.cron); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, sc)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	now := s.now()
	minute := now.Truncate(time.Minute)
	fired := 0
	for _, sc := range all {
		sched, err := jobs.Parse(sc.cron)
		if err != nil {
			// A row the panel would not create today: named, skipped and left visible rather than silently
			// dropped, so the operator can see the schedule is not running.
			slog.Warn("backup schedule has an expression the panel cannot parse", "schedule", sc.id, "cron", sc.cron, "err", err)
			continue
		}
		if !sched.Matches(minute) {
			continue
		}
		var opened int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE schedule_id=? AND started_at>=?`, sc.id, s.ts(minute)).Scan(&opened); err != nil {
			return fired, err
		}
		if opened > 0 {
			continue
		}
		err = store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
			run, err := s.StartRun(ctx, tx, sc.id, TriggerSchedule)
			if err != nil {
				return err
			}
			_, err = q.Enqueue(ctx, tx, JobRun, run.ID, now)
			return err
		})
		var cf Conflict
		if errors.As(err, &cf) {
			// A manual run of this schedule is in progress. Two dumps of one database at once is the memory
			// incident StartRun refuses, so this slot passes and the next one is tried again.
			slog.Info("scheduled backup skipped: an attempt is already running", "schedule", sc.id)
			continue
		}
		if err != nil {
			return fired, err
		}
		fired++
	}
	return fired, nil
}

// Recover closes the attempts a previous daemon process left running.
//
// A dump whose process died has no writer, and an open run row blocks that schedule forever: StartRun refuses a
// second concurrent attempt, so one crash at 03:00 would silently stop every later backup. The row cannot be
// deleted — it is the operator's record that an attempt happened — so it becomes a failure with the reason.
func (s *Service) Recover(ctx context.Context) (int, error) {
	var ids []string
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM backup_runs WHERE status='running' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, id := range ids {
		s.failRun(ctx, id, errors.New("the daemon stopped while this backup was running, so its result was never recorded"))
	}
	if len(ids) > 0 {
		slog.Info("closed backup attempts left running by a previous process", "count", len(ids))
	}
	return len(ids), nil
}

// PreDeployBackup takes the snapshot a deploy of a database resource needs before its container is replaced.
//
// It returns false when the resource has no schedule: an application container is not a database, and a deploy of
// one is not a data-loss event. When a schedule does exist the dump is synchronous, because "we backed it up
// before we replaced you" is only a promise if the bytes are on disk before the old container stops.
//
// The first schedule by id defines the destination and the retention window. A resource with several schedules
// needs one pre-deploy snapshot, not one per schedule: they are copies of the same database taken in the same
// second, and the later sweeps would each see only their own.
func (s *Service) PreDeployBackup(ctx context.Context, resourceID string) (bool, error) {
	var scheduleID string
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM backup_schedules WHERE resource_id=? ORDER BY id LIMIT 1`, resourceID).Scan(&scheduleID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var runID string
	if err := store.InTx(ctx, s.DB, func(tx *sql.Tx) error {
		run, err := s.StartRun(ctx, tx, scheduleID, TriggerPreDeploy)
		if err != nil {
			return err
		}
		runID = run.ID
		return nil
	}); err != nil {
		return false, err
	}
	if err := s.Execute(ctx, runID); err != nil {
		return false, err
	}
	rn, err := s.GetRun(ctx, s.DB, runID)
	if err != nil {
		return false, err
	}
	if rn.Status != "success" {
		// Execute records the failure and returns nil; the deploy has to hear about it, because proceeding now
		// would replace a database the panel just failed to copy.
		return false, fmt.Errorf("the pre-deploy backup of this database failed: %s", orString(rn.Error, "no reason was recorded"))
	}
	return true, nil
}

// PruneAll ages out every schedule's expired attempts, once per minute alongside ScheduleTick.
//
// Retention lives here rather than at the end of a successful dump because it is the schedule's rule, not the
// attempt's: a schedule whose next run never happens (the resource was deleted, the module switched off) still has
// to stop holding its old dumps. One sweep per minute is also what makes the window an operator sets in the panel
// true within a minute of them setting it.
func (s *Service) PruneAll(ctx context.Context) (int, error) {
	schedules, err := s.ListSchedules(ctx, s.DB, "")
	if err != nil {
		return 0, err
	}
	total := 0
	for _, sc := range schedules {
		n, err := s.PruneSchedule(ctx, s.DB, sc)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

func orString(v *string, def string) string {
	if v == nil || *v == "" {
		return def
	}
	return *v
}
