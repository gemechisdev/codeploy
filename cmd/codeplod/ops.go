// P6 operations wiring: the four services the panel's operations pages talk to, and the loops that keep them
// running. Everything here is constructed before the job queue starts, because two of these services register a
// queue handler and one of them has boot recovery that must finish before a worker could pick up its work.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/api"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/modules/cron"
	"github.com/codeplo/codeplo/internal/modules/databases"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/mcp"
	"github.com/codeplo/codeplo/internal/modules/monitor"
	"github.com/codeplo/codeplo/internal/modules/notify"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/objectstore"
	"github.com/codeplo/codeplo/internal/store"
)

// opsServices holds the operations services once they exist. A nil field means its module is switched off, which is
// what every consumer here and in the API treats as "not available" rather than as "no results".
type opsServices struct {
	monitor   *monitor.Service
	databases *databases.Service
	cron      *cron.Service
	// PreDeploy backs a resource's database up before a deploy replaces its container. nil when the databases
	// module is off: the deploy engine then logs that no backup was possible and continues, because a panel that
	// cannot deploy without a database module would be a worse failure than one without the safety net. It returns
	// whether a snapshot was actually taken, which is what the deployment log says either way.
	PreDeploy func(context.Context, string) (bool, error)
}

// wireOps builds the operations services from the daemon's own configuration and hands the API-facing ones to the
// server. It fails only on a destination the operator asked for and the daemon cannot use — see the S3 note below.
func wireOps(ctx context.Context, srv *api.Server, cfg config.Config, db *sql.DB, key []byte, docker *runtime.Docker) (*opsServices, error) {
	o := &opsServices{}

	// Channels are sealed with the master key and need no runtime, so they wire first and the alert evaluator can
	// be pointed at them even on a host with no Docker.
	notifySvc := notify.New(db, key)
	notifySvc.Timeout = time.Duration(settingInt(ctx, db, "notify.timeout_s", 10)) * time.Second
	srv.Notify = notifySvc

	if srv.Reg.Enabled(ctx, "monitor") {
		m := monitor.New(db)
		// Containers is a narrower port than Manager, and Docker satisfies it: measuring a host must not require
		// the daemon to have said it is allowed to create containers.
		if docker != nil {
			m.Runtime = docker
		}
		m.Notify = notifySvc
		srv.Monitor = m
		o.monitor = m
	}

	if srv.Reg.Enabled(ctx, "databases") {
		d := databases.New(db, key)
		d.DataDir = cfg.DataDir
		d.Timeout = time.Duration(settingInt(ctx, db, "backup.timeout_s", 900)) * time.Second
		if docker != nil {
			d.Runtime = docker
		}
		client, configured, err := s3Client()
		if err != nil {
			return nil, err
		}
		d.S3 = client
		if !configured {
			slog.Info("no s3 destination configured: backup schedules of that kind will fail with a stated reason")
		}
		o.databases = d
		o.PreDeploy = d.PreDeployBackup
		srv.Databases = d
		// The queue reads its handler map while running, so registration happens here rather than at start.
		// A run row left 'running' by a crash is failed by recoverBackups below, before the worker starts: the
		// handler is idempotent, but the schedule that owns the row would otherwise refuse its next attempt.
		srv.Jobs.Register(databases.JobRun, d.Execute)
		if n, err := d.Recover(ctx); err != nil {
			return nil, err
		} else if n > 0 {
			slog.Warn("closed backup attempts left running by a previous daemon", "count", n)
		}
	}

	// Cron jobs are stored per resource and executed inside its container. There is no API route for them (the
	// contract has none) — this service exists for the loop below and for `codeplo cron`.
	c := cron.New(db, key)
	if docker != nil {
		c.Runtime = docker
	}
	o.cron = c

	return o, nil
}

// s3Client maps the daemon's CODEPLO_S3_* environment onto the object store. A half-configured destination is a
// boot error rather than a silent downgrade to local-only: an operator who named a bucket believes their dumps are
// off-host, and a schedule that fails at 03:00 on a missing secret is worse than a daemon that refuses to start.
func s3Client() (*objectstore.Client, bool, error) {
	v, configured, err := config.LoadS3()
	if err != nil || !configured {
		return nil, configured, err
	}
	client, err := objectstore.New(objectstore.Config{
		Endpoint: v.Endpoint, Region: v.Region, Bucket: v.Bucket,
		AccessKey: v.AccessKey, SecretKey: v.SecretKey, Prefix: v.Prefix,
	})
	if err != nil {
		return nil, true, errors.Join(errors.New("the configured s3 backup destination is not usable"), err)
	}
	// The bucket name is not a secret and the operator needs one line confirming which destination the daemon
	// believes its s3 schedules point at; the key pair is never printed.
	slog.Info("s3 backup destination configured", "endpoint", v.Endpoint, "bucket", v.Bucket, "prefix", v.Prefix)
	return client, true, nil
}

// startLoops runs the operations tickers. Each loop re-reads its own interval from settings on every pass, so a
// change made in the settings page takes effect on the next tick instead of needing a daemon restart.
//
// logSweep is E7 — the deploy engine's retention pass over deployment_logs. It is a parameter rather than a field
// because it belongs to the workload module, not to operations: the daemon hands it over when a deploy engine
// exists, and a panel with the deploy module off has no deployment logs to sweep.
func (o *opsServices) startLoops(ctx context.Context, srv *api.Server, q *jobs.Queue, publish func(string, any), logSweep func(context.Context) error) {
	if o.monitor != nil {
		m := o.monitor
		// A resource deleted while its points were still in the ring would otherwise leave rows nothing can
		// address; this is the one pass that can only be correct at boot, since the window it looks at is "now".
		if n, err := m.PruneOrphans(ctx, srv.DB); err != nil {
			slog.Warn("metric orphan sweep failed", "err", err)
		} else if n > 0 {
			slog.Info("dropped metric samples for resources that no longer exist", "count", n)
		}
		go func() {
			for {
				if !sleepCtx(ctx, time.Duration(m.IntervalS(ctx))*time.Second) {
					return
				}
				pt, err := m.SampleOnce(ctx)
				if err != nil {
					// A failed sample is a gap in the chart, not a stopped daemon: the next tick tries again.
					slog.Warn("metric sample failed", "err", err)
					continue
				}
				// The contract's metrics.sample event carries HostMetrics, which is exactly the stored point.
				publish("metrics.sample", pt)
				if _, err := m.Prune(ctx, srv.DB, m.MaxPoints(ctx), m.IntervalS(ctx)); err != nil {
					slog.Warn("metric ring prune failed", "err", err)
				}
			}
		}()
		// The evaluator has its own interval because detection touches every rule and every firing, and an
		// operator who wants a finer chart should not have to pay for it in alert queries.
		go func() {
			for {
				if !sleepCtx(ctx, time.Duration(m.EvalIntervalS(ctx))*time.Second) {
					return
				}
				if err := m.Evaluate(ctx); err != nil {
					slog.Warn("alert evaluation pass failed", "err", err)
				}
			}
		}()
	}

	if o.databases != nil {
		d := o.databases
		// Both backup loops work off the same minute boundary: the scheduler decides when a schedule is due, the
		// retention pass removes attempts older than their schedule's window.
		go everyMinute(ctx, "backup schedule", func(c context.Context) error { _, err := d.ScheduleTick(c, q); return err })
		go everyMinute(ctx, "backup retention", func(c context.Context) error { _, err := d.PruneAll(c); return err })
	}

	if o.cron != nil {
		c := o.cron
		go everyMinute(ctx, "resource cron", func(ctx context.Context) error { _, err := c.Tick(ctx); return err })
	}

	if logSweep != nil {
		go everyInterval(ctx, "log retention", time.Hour, logSweep)
	}
}

// sleepCtx waits d, and reports false when the daemon is shutting down instead.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// everyMinute runs pass on a one-minute cadence aligned to the clock, so the minute-resolution schedules in the
// database and the daemon's tick agree about which minute is being evaluated.
func everyMinute(ctx context.Context, name string, pass func(context.Context) error) {
	for {
		now := time.Now()
		d := time.Until(now.Truncate(time.Minute).Add(time.Minute)) + 500*time.Millisecond
		if !sleepCtx(ctx, d) {
			return
		}
		c, cancel := context.WithTimeout(ctx, 55*time.Second)
		err := pass(c)
		cancel()
		if err != nil {
			slog.Warn(name+" pass failed", "err", err)
		}
	}
}

// everyInterval runs pass on a fixed cadence, with the pass bounded so a sweep that hangs on the database cannot
// also hold the daemon's shutdown.
func everyInterval(ctx context.Context, name string, d time.Duration, pass func(context.Context) error) {
	for {
		if !sleepCtx(ctx, d) {
			return
		}
		c, cancel := context.WithTimeout(ctx, d-500*time.Millisecond)
		err := pass(c)
		cancel()
		if err != nil {
			slog.Warn(name+" pass failed", "err", err)
		}
	}
}

func settingInt(ctx context.Context, db *sql.DB, key string, def int) int {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), `" `))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// ---- MCP (T59): a flag-gated, daemon-local NDJSON JSON-RPC host on a unix socket ----
//
// T53 forbids new OpenAPI routes, so MCP is a unix socket the daemon owns, not an HTTP path. The socket is created
// once and kept regardless of the module flag: the flag is re-read on every connection (and every request inside a
// connection) so an operator enabling "mcp" / "mcp.server" takes effect without a daemon restart, and disabling it
// makes the server refuse with a message rather than disappear.

type mcpDeps struct {
	db   *sql.DB
	base *mcp.Server // the tool closures and DB; CheckPerm is bound per connection
}

// newMCP builds the tool host with closures over the daemon's real stores and services. A closure is left nil when
// its dependency is genuinely unavailable, so the tool answers "not available" instead of faking an empty success.
func newMCP(db *sql.DB, srv *api.Server) *mcp.Server {
	s := mcp.New(db)
	s.ListProjects = func(ctx context.Context) ([]map[string]string, error) {
		// Same SELECT (api.Server).listProjects uses.
		rows, err := db.QueryContext(ctx, `SELECT id, name, slug, description, created_at FROM projects ORDER BY id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []map[string]string
		for rows.Next() {
			var id, name, slug, desc, created string
			if err := rows.Scan(&id, &name, &slug, &desc, &created); err != nil {
				return nil, err
			}
			out = append(out, map[string]string{"id": id, "name": name, "slug": slug, "description": desc, "created_at": created})
		}
		return out, rows.Err()
	}
	s.ListResources = func(ctx context.Context) ([]map[string]string, error) {
		// Same FROM/JOIN and leading columns (api.Server).listResources uses. The computed status CASE is the
		// handler's business logic, so this reader returns stored columns instead of duplicating it.
		rows, err := db.QueryContext(ctx, `SELECT r.id, r.environment_id, e.project_id, e.name, r.type, r.name, r.slug, r.desired_state, r.created_at FROM resources r JOIN environments e ON e.id=r.environment_id ORDER BY r.id`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []map[string]string
		for rows.Next() {
			var id, envID, projID, envName, typ, name, slug, desired, created string
			if err := rows.Scan(&id, &envID, &projID, &envName, &typ, &name, &slug, &desired, &created); err != nil {
				return nil, err
			}
			out = append(out, map[string]string{"id": id, "environment_id": envID, "project_id": projID,
				"environment_name": envName, "type": typ, "name": name, "slug": slug, "desired_state": desired, "created_at": created})
		}
		return out, rows.Err()
	}
	// The deploy engine is the real service behind a deploy. It exists only with the deploy module on and a runtime
	// wired; without it TriggerDeploy stays nil and the tool reports "deploy is not available".
	if eng := srv.Deploy; eng != nil {
		s.TriggerDeploy = func(ctx context.Context, resourceID string) (string, error) {
			var cfgJSON string
			if err := db.QueryRowContext(ctx, `SELECT config_json FROM resources WHERE id=?`, resourceID).Scan(&cfgJSON); err != nil {
				return "", err
			}
			cfg, err := deploy.ParseConfig(cfgJSON)
			if err != nil {
				return "", err
			}
			mode := cfg.Image.Mode
			imageRef := ""
			if mode == "pull" && cfg.Image.Ref != nil {
				imageRef = *cfg.Image.Ref
			}
			var branch sql.NullString
			if mode == "build" {
				// Same source query (api.Server).createDeployment uses for a build.
				_ = db.QueryRowContext(ctx, `SELECT branch FROM resource_sources WHERE resource_id=?`, resourceID).Scan(&branch)
			}
			if v, err := eng.CheckPolicy(ctx, resourceID); err != nil {
				return "", err
			} else if len(v) > 0 {
				return "", errors.New("isolation policy rejected this resource")
			}
			var depID string
			// Reuse the engine's own Create (the same call the deployment handler makes) rather than
			// re-implementing deployment creation. store.InTx matches the handler's transaction boundary.
			err = store.InTx(ctx, db, func(tx *sql.Tx) error {
				var e error
				depID, e = eng.Create(ctx, tx, srv.Jobs, deploy.NewDeployment{
					ResourceID: resourceID, Trigger: deploy.TrManual, Mode: mode, Branch: branch.String, ImageRef: imageRef,
				})
				return e
			})
			return depID, err
		}
	}
	return s
}

// startMCP creates the 0600 unix socket under the data dir and supervises connections until ctx is cancelled.
func startMCP(ctx context.Context, cfg config.Config, db *sql.DB, srv *api.Server) (string, error) {
	path := mcp.SocketPath(cfg.DataDir)
	_ = os.Remove(path) // a stale socket left by a crashed daemon
	ln, err := net.Listen("unix", path)
	if err != nil {
		return path, err
	}
	// 0600: only the service user that owns the daemon (and therefore the socket) can connect.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return path, err
	}
	d := &mcpDeps{db: db, base: newMCP(db, srv)}
	// Close the listener on shutdown so Accept unblocks and the accept loop exits (no goroutine leak).
	context.AfterFunc(ctx, func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(ctx, c)
		}
	}()
	slog.Info("mcp server", "socket", path, "enabled", d.base.Enabled(ctx))
	return path, nil
}

func (d *mcpDeps) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	// On shutdown, close this connection so a blocking decode in Serve returns and the goroutine exits.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	// Gate re-read at accept time: enable/disable takes effect on the next connection, no restart.
	if !d.base.Enabled(ctx) {
		fmt.Fprintln(conn, "mcp: refused - the mcp module or the mcp.server feature flag is disabled")
		return
	}
	// Bind this connection to a principal from the token read at accept time. No token (or an invalid one) means
	// no principal, and the connection is refused with a sentence that never includes the token itself.
	perms, err := mcp.ResolvePrincipal(ctx, d.db, os.Getenv(mcp.TokenEnv))
	if err != nil {
		fmt.Fprintln(conn, "mcp: refused - "+err.Error())
		return
	}
	s := *d.base // a shallow copy: tool closures are shared, CheckPerm is per connection
	s.CheckPerm = mcp.Checker(perms)
	if err := s.Serve(ctx, conn, conn); err != nil {
		if errors.Is(err, mcp.ErrDisabled) {
			fmt.Fprintln(conn, "mcp: refused - the mcp module was disabled")
		} else {
			slog.Debug("mcp connection ended", "err", err)
		}
	}
}
