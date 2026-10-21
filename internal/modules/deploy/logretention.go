// E7: the retention pass over deployment_logs.
//
// Two bounds, because the failure modes are different. A deployment log left forever grows the table without limit
// even though no one reads it — that is age. A single runaway build can print a million lines in an hour, which age
// does not help with at all — that is the total-row budget. The per-deployment line cap the writer already applies
// (deploy.max_log_lines) stops one deployment from being the whole table; this pass is what stops the table from
// being the whole disk.
//
// Both passes delete whole deployments' logs, never individual lines inside one deployment. A half-deleted build log
// is worse than a missing one: the operator reading it cannot tell whether the build produced no output after line
// 400 or whether the panel lost it.
package deploy

import (
	"context"
	"log/slog"
	"time"
)

// The defaults the settings rows seed too, and the values used when a settings row is missing or unreadable: a
// retention pass must never fail because of a absent preference, it just falls back.
const (
	defaultLogRetentionDays = 14
	defaultLogMaxRows       = 200000
)

// PruneLogs applies both bounds and returns how many rows it removed.
//
// A deployment is exempt from both passes when it is the resource's active deployment or when its image is retained
// (the rollback targets). Those are exactly the two logs an operator is pointed at — "what is running now" and
// "what did the version you are rolling back to print" — and a budget that threw them away to keep ten old build
// logs would be optimising the wrong thing. A deployment that has not finished is never touched at all, because its
// writer is still appending to it.
func (e *Engine) PruneLogs(ctx context.Context) (int, error) {
	days := e.settingInt(ctx, "logs.retention_days", defaultLogRetentionDays)
	maxRows := e.settingInt(ctx, "logs.max_rows", defaultLogMaxRows)

	// Age. finished_at is the guard that makes this pass safe to run while a deploy is in progress: a row that has
	// no end time is a row whose log is still being written.
	cutoff := ts(e.cfg.Now().Add(-24 * time.Duration(days) * time.Hour))
	res, err := e.db.ExecContext(ctx, `DELETE FROM deployment_logs WHERE deployment_id IN (
		SELECT d.id FROM deployments d
		WHERE d.finished_at IS NOT NULL AND d.finished_at<? AND d.retained=0 AND NOT EXISTS (
			SELECT 1 FROM resources r WHERE r.active_deployment_id=d.id))`, cutoff)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()

	var total int
	if err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM deployment_logs`).Scan(&total); err != nil {
		return int(deleted), err
	}
	if total <= maxRows {
		return int(deleted), nil
	}

	// Budget. The cursor is drained before anything is deleted: this is the same database handle, and the pass must
	// not decide what to drop from a view of the table it is midway through changing.
	type victim struct {
		id    string
		count int
	}
	rows, err := e.db.QueryContext(ctx, `SELECT d.id, COUNT(l.deployment_id) FROM deployments d
		JOIN deployment_logs l ON l.deployment_id=d.id
		WHERE d.finished_at IS NOT NULL AND d.retained=0 AND NOT EXISTS (
			SELECT 1 FROM resources r WHERE r.active_deployment_id=d.id)
		GROUP BY d.id ORDER BY d.started_at, d.id`)
	if err != nil {
		return int(deleted), err
	}
	var victims []victim
	for rows.Next() {
		var v victim
		if err := rows.Scan(&v.id, &v.count); err != nil {
			rows.Close()
			return int(deleted), err
		}
		victims = append(victims, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return int(deleted), err
	}

	// Oldest first, and it stops the moment the table fits: the point is a bounded table, not an empty one.
	over := total - maxRows
	for _, v := range victims {
		if over <= 0 {
			break
		}
		if _, err := e.db.ExecContext(ctx, `DELETE FROM deployment_logs WHERE deployment_id=?`, v.id); err != nil {
			return int(deleted), err
		}
		deleted += int64(v.count)
		over -= v.count
	}
	if over > 0 {
		// Every exempt deployment is still needed to reach the budget. Saying so is the whole answer: the operator
		// can raise logs.max_rows or retire a rollback target, but the pass will not invent room by cutting into
		// the logs that are still in use.
		slog.Warn("log retention could not reach its row budget without touching active or retained deployments",
			"rows_over", over, "retention_days", days, "max_rows", maxRows)
	}
	return int(deleted), nil
}
