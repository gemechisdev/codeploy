// Package monitor is Phase 6's measurement and alarm layer: the metrics ring buffer the charts read, the sampler
// that fills it, and the alert evaluator that decides when something is worth telling a human about.
//
// Two rules hold it together. Every number in here is a measurement or a delta of measurements — nothing is
// estimated, and a source that is unavailable is recorded as zero with a warning rather than as a plausible
// guess. And the evaluator's output is a *firing*, not a notification: dedupe and cooldown belong to the state
// machine, so a rule that keeps failing on one resource says so once and then again only on the rule's own
// schedule.
//
// HTTP is internal/api/handlers_monitor.go. Delivery is internal/modules/notify; this package calls it and never
// reimplements it.
package monitor

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/modules/notify"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/platform"
)

// Metric is one stored point, and the contract's HostMetrics verbatim: the columns of metrics_samples are these
// fields, so reading a chart is a SELECT and never a mixture of history and live values.
//
// The five host-only fields (daemon_rss_mb, disk_used_gb, disk_total_gb) are zero on a resource point. A container
// has no disk total and no Docker daemon inside it, and inventing a value for them would put a number in a chart
// that no measurement produced.
type Metric struct {
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsedMB  int     `json:"memory_used_mb"`
	MemoryTotalMB int     `json:"memory_total_mb"`
	DaemonRSSMB   float64 `json:"daemon_rss_mb"`
	DiskUsedGB    float64 `json:"disk_used_gb"`
	DiskTotalGB   float64 `json:"disk_total_gb"`
	NetRxKbps     float64 `json:"net_rx_kbps"`
	NetTxKbps     float64 `json:"net_tx_kbps"`
	ContainerCnt  int     `json:"container_count"`
	At            string  `json:"at"`
}

// The two scopes the table holds. A resource point's ref_id is its resource id; the host series has no ref_id.
const (
	ScopeHost     = "host"
	ScopeResource = "resource"
)

// sampleIDPrefix is every metrics row's id prefix, shared by the insert and the range bound so the two cannot
// drift apart into different orderings.
const sampleIDPrefix = "mts_"

// Querier is satisfied by both *sql.DB and *sql.Tx, so a sample can commit inside the caller's transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func settingInt(ctx context.Context, q Querier, key string, def int) int {
	var v string
	if err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// Insert stores one point. id is the ULID the series is ordered by: it sorts with time, so "the newest N points"
// is an index scan rather than a scan of the whole table, and the prune below uses the same key.
func (s *Service) Insert(ctx context.Context, q Querier, scope, refID string, m Metric) error {
	if scope != ScopeHost && scope != ScopeResource {
		return errors.New("monitor: unknown metric scope " + scope)
	}
	_, err := q.ExecContext(ctx, `INSERT INTO metrics_samples(
		id,at,scope,ref_id,cpu_percent,memory_used_mb,memory_total_mb,daemon_rss_mb,disk_used_gb,disk_total_gb,net_rx_kbps,net_tx_kbps,container_count)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		crypto.ULID(sampleIDPrefix, s.now().UnixMilli()), m.At, scope, refID,
		m.CPUPercent, m.MemoryUsedMB, m.MemoryTotalMB, m.DaemonRSSMB,
		m.DiskUsedGB, m.DiskTotalGB, m.NetRxKbps, m.NetTxKbps, m.ContainerCnt)
	return err
}

// SeriesSince returns the points of one series inside a time window, oldest first: a chart reads left to right,
// and doing that reversal here means every caller does not get it wrong on its own.
//
// limit bounds the read, not the window. The panel's own ceiling is 240 points, which is what the mock returns for
// a 6-hour range, so a shorter sample interval yields the newest part of the window rather than a heavier payload.
//
// The window filter is on the id, not the `at` column: ids are time-ordered, so a bound at the instant the window
// opens is an index seek that stops at the oldest point it needs, while a timestamp filter would read the whole
// series to work out which rows to drop. The bound is one ULID of resolution (a millisecond), which is three
// orders of magnitude finer than the shortest sample interval.
func (s *Service) SeriesSince(ctx context.Context, q Querier, scope, refID string, since time.Time, limit int) ([]Metric, error) {
	if limit <= 0 {
		limit = 240
	}
	rows, err := q.QueryContext(ctx, `SELECT cpu_percent,memory_used_mb,memory_total_mb,daemon_rss_mb,disk_used_gb,disk_total_gb,net_rx_kbps,net_tx_kbps,container_count,at FROM (
		SELECT * FROM metrics_samples WHERE scope=? AND ref_id=? AND id>? ORDER BY id DESC LIMIT ?) ORDER BY id`,
		scope, refID, crypto.ULIDFloor(sampleIDPrefix, since.UnixMilli()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Metric{}
	for rows.Next() {
		var m Metric
		if err := rows.Scan(&m.CPUPercent, &m.MemoryUsedMB, &m.MemoryTotalMB, &m.DaemonRSSMB,
			&m.DiskUsedGB, &m.DiskTotalGB, &m.NetRxKbps, &m.NetTxKbps, &m.ContainerCnt, &m.At); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Latest returns the newest point of one series, or ok=false when it has none.
func (s *Service) Latest(ctx context.Context, q Querier, scope, refID string) (Metric, bool, error) {
	var m Metric
	err := q.QueryRowContext(ctx, `SELECT cpu_percent,memory_used_mb,memory_total_mb,daemon_rss_mb,disk_used_gb,disk_total_gb,net_rx_kbps,net_tx_kbps,container_count,at FROM metrics_samples
		WHERE scope=? AND ref_id=? ORDER BY id DESC LIMIT 1`, scope, refID).
		Scan(&m.CPUPercent, &m.MemoryUsedMB, &m.MemoryTotalMB, &m.DaemonRSSMB, &m.DiskUsedGB, &m.DiskTotalGB,
			&m.NetRxKbps, &m.NetTxKbps, &m.ContainerCnt, &m.At)
	if errors.Is(err, sql.ErrNoRows) {
		return Metric{}, false, nil
	}
	if err != nil {
		return Metric{}, false, err
	}
	return m, true, nil
}

// Containers is the slice of the container port the sampler needs: which containers this panel owns, and what one
// of them is using. It is *runtime.Docker in production and a fixture in a test, and it is deliberately not
// runtime.Manager — measuring a host does not need the lifecycle half of that port, and asking for less means the
// monitor can be wired on a machine where container creation is refused.
type Containers interface {
	ListContainers(ctx context.Context, labelKey string) ([]runtime.State, error)
	Stats(ctx context.Context, container string) (runtime.ContainerStats, error)
}

// Service is the monitor's entry point. Everything it measures comes from an injected source, so a test can drive
// the whole sampler and evaluator without a host, a daemon or a network.
type Service struct {
	DB *sql.DB
	// Now is the clock the samples are stamped with; injectable so a test can place a point in a window.
	Now func() time.Time
	// Host reports RAM, disk and the daemon's own resident memory. Defaults to platform.Host.
	Host func() platform.HostInfo
	// Sample reports the cumulative CPU ticks and network counters the rates are differenced from. Defaults to
	// platform.Sample.
	Sample func() platform.Snapshot
	// Runtime measures containers. Nil means this host has no Docker, and the sampler records zero containers with
	// a warning rather than the last count it saw.
	Runtime Containers
	// Notify delivers alert messages. Nil means alerts are recorded and published but nobody is messaged, which is
	// what a panel with no channels configured does.
	Notify *notify.Service

	// prev is the last host counter reading. CPU percentage and network rate are deltas, so the first sample after
	// a start has nothing to difference against and reports zero; the sampler is single-goroutine, so these need no
	// lock.
	prev   platform.Snapshot
	prevAt time.Time
}

func New(db *sql.DB) *Service { return &Service{DB: db} }

func (s *Service) host() platform.HostInfo {
	if s.Host != nil {
		return s.Host()
	}
	return platform.Host()
}

func (s *Service) counters() platform.Snapshot {
	if s.Sample != nil {
		return s.Sample()
	}
	return platform.Sample()
}

// IntervalS is the configured sample interval, and the value the daemon's ticker is built from.
func (s *Service) IntervalS(ctx context.Context) int {
	return settingInt(ctx, s.DB, "metrics.sample_interval_s", 60)
}

// MaxPoints is the configured ring-buffer depth.
func (s *Service) MaxPoints(ctx context.Context) int {
	return settingInt(ctx, s.DB, "metrics.max_samples", 20160)
}

// EvalIntervalS is how often the alert evaluator runs. It is its own setting rather than the sample interval
// because detection cost is not sampling cost: an evaluator reads every rule and every firing, while a sampler
// reads two /proc files.
func (s *Service) EvalIntervalS(ctx context.Context) int {
	return settingInt(ctx, s.DB, "alerts.eval_interval_s", 60)
}

// RangePoints is the mock's range table: the chart asks for a window and gets this many points at the default
// 60-second interval, capped at 240 so a 6-hour and a 24-hour range cost the same to send.
func RangePoints(r string) (points int, ok bool) {
	switch r {
	case "15m":
		return 15, true
	case "1h":
		return 60, true
	case "6h", "24h":
		return 240, true
	}
	return 0, false
}

// RangeWindow is the same four ranges as durations, which is how the handler turns a request into a since time.
func RangeWindow(r string) (time.Duration, bool) {
	switch r {
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "6h":
		return 6 * time.Hour, true
	case "24h":
		return 24 * time.Hour, true
	}
	return 0, false
}

// Prune drops every point older than the window the settings describe, and returns how many went.
//
// metrics.max_samples is a count and a count is awkward to enforce cheaply: "delete everything but the newest
// 20160 rows of each series" means either a 20160-value subquery per series per tick or an OFFSET scan of the same
// length. The same policy as a time window — the cap times the sample interval, which is 20160 minutes, or
// fourteen days, at the default interval — is one index seek to an id bound and a range delete after it, so it
// costs what it removes and nothing else.
//
// The consequence is honest and worth stating: change the sample interval and the history window changes with it,
// because the cap is in samples. That is the unit the operator configured.
func (s *Service) Prune(ctx context.Context, q Querier, maxPoints, intervalS int) (int, error) {
	if maxPoints <= 0 || intervalS <= 0 {
		return 0, nil
	}
	age := time.Duration(maxPoints*intervalS) * time.Second
	res, err := q.ExecContext(ctx, `DELETE FROM metrics_samples WHERE id<?`,
		crypto.ULIDFloor(sampleIDPrefix, s.now().Add(-age).UnixMilli()))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PruneOrphans drops resource series whose resource is gone: a deleted resource has no chart to fill, and its
// points would otherwise sit in the window until their age evicted them.
//
// It is separate from Prune because it is the expensive half — it has to look at every resource-scope row — and it
// belongs at boot and after a resource deletion rather than on the sampler's tick.
func (s *Service) PruneOrphans(ctx context.Context, q Querier) (int, error) {
	res, err := q.ExecContext(ctx, `DELETE FROM metrics_samples WHERE scope='resource' AND ref_id NOT IN (SELECT id FROM resources)`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
