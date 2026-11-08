package edge

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/codeplo/codeplo/internal/platform"
)

// ErrCaddyDown is returned when the admin API cannot be reached even after a supervisor restart attempt.
var ErrCaddyDown = errors.New("caddy is not running")

// Status mirrors the OpenAPI EdgeStatus object.
type Status struct {
	CaddyRunning     bool    `json:"caddy_running"`
	AdminSocket      string  `json:"admin_socket"`
	ConfigHash       string  `json:"config_hash"`
	LastReconciledAt *string `json:"last_reconciled_at"`
	Drift            bool    `json:"drift"`
	RouteCount       int     `json:"route_count"`
	ServerNow        string  `json:"server_now"`
}

// Cert mirrors the OpenAPI Certificate object.
type Cert struct {
	Hostname string  `json:"hostname"`
	Issuer   string  `json:"issuer"`
	Status   string  `json:"status"`
	NotAfter *string `json:"not_after"`
	Error    *string `json:"error"`
}

// Reconciler renders the desired config from the domains table and pushes it to Caddy when it differs.
type Reconciler struct {
	DB       *sql.DB
	Client   *Client
	Run      platform.Runner
	Opts     Options
	DataDir  string // Caddy storage dir (certificates live under it)
	Unit     string // systemd unit used by the supervisor (default codeplo-caddy)
	mu       sync.Mutex
	failures int
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (r *Reconciler) sites(ctx context.Context) ([]Site, error) {
	// A resource's upstream is derived from resources.active_deployment_id and nothing else (plan 3.8), so a
	// deployment that never reached the switch step cannot move traffic. Two states are then distinguished, and
	// getting them wrong has opposite costs:
	//
	//   - desired_state='stopped' means the operator stopped it. No route is correct: the alternative is a route
	//     to a container that is deliberately not running.
	//   - running with an empty lookup is a transient state (a pointer set by recovery, a deployment row that has
	//     not recorded its address yet). Serving the last address domains.upstream holds -- written in the same
	//     transaction as the pointer, and refreshed at boot when the container's address moved -- keeps a live
	//     site up through the moment instead of 404ing it.
	//
	// Rows whose resource_id has no resources row (the pre-P4 shape, kept for fixtures) fall back to the stored upstream.
	rows, err := r.DB.QueryContext(ctx, `SELECT d.hostname, d.kind, d.force_https, d.redirect_www,
 CASE WHEN d.kind='panel' THEN d.upstream
      WHEN NOT EXISTS (SELECT 1 FROM resources r WHERE r.id=d.resource_id) THEN d.upstream
      WHEN NOT EXISTS (SELECT 1 FROM resources r WHERE r.id=d.resource_id AND r.desired_state='running') THEN ''
      ELSE COALESCE(NULLIF((SELECT dep.upstream FROM resources r JOIN deployments dep ON dep.id=r.active_deployment_id
                            WHERE r.id=d.resource_id), ''), d.upstream)
 END
 FROM domains d ORDER BY d.hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Site, 0)
	for rows.Next() {
		var s Site
		var kind string
		var f, w int
		if err := rows.Scan(&s.Hostname, &kind, &f, &w, &s.Upstream); err != nil {
			return nil, err
		}
		s.Panel, s.ForceHTTPS, s.RedirectWWW = kind == "panel", f == 1, w == 1
		// Empty after the derivation above means "stopped, or never successfully deployed": no route to render.
		if !s.Panel && strings.TrimSpace(s.Upstream) == "" {
			continue
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Supervise starts the Caddy unit when the admin API is unreachable. It returns whether Caddy is healthy afterwards.
func (r *Reconciler) Supervise(ctx context.Context) bool {
	if r.Client.Healthy(ctx) {
		return true
	}
	unit := r.Unit
	if unit == "" {
		unit = "codeplo-caddy"
	}
	if r.Run != nil {
		_, _ = r.Run(ctx, "systemctl", "restart", unit)
	}
	for i := 0; i < 10; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
		if r.Client.Healthy(ctx) {
			return true
		}
	}
	return false
}

// Status reports the current state without changing anything.
func (r *Reconciler) Status(ctx context.Context) (Status, error) {
	st := Status{AdminSocket: r.Opts.AdminSocket, ServerNow: iso(time.Now())}
	sites, err := r.sites(ctx)
	if err != nil {
		return st, err
	}
	want, hash, err := Render(sites, r.Opts)
	if err != nil {
		return st, err
	}
	st.ConfigHash, st.RouteCount = hash, len(sites)
	var last, at sql.NullString
	_ = r.DB.QueryRowContext(ctx, `SELECT last_hash, last_reconciled_at FROM edge_state WHERE id=1`).Scan(&last, &at)
	if at.Valid {
		st.LastReconciledAt = &at.String
	}
	st.CaddyRunning = r.Client.Healthy(ctx)
	if st.CaddyRunning {
		if run, err := r.Client.Running(ctx); err == nil {
			st.Drift = Canonical(run) != Canonical(want)
		}
	} else {
		st.Drift = true
	}
	return st, nil
}

// Reconcile pushes the desired config to Caddy if it drifted, then refreshes certificate status.
func (r *Reconciler) Reconcile(ctx context.Context) (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sites, err := r.sites(ctx)
	if err != nil {
		return Status{}, err
	}
	want, hash, err := Render(sites, r.Opts)
	if err != nil {
		return Status{}, err
	}
	if !r.Supervise(ctx) {
		st, _ := r.Status(ctx)
		return st, ErrCaddyDown
	}
	if run, err := r.Client.Running(ctx); err != nil || Canonical(run) != Canonical(want) {
		if err := r.Client.Load(ctx, want); err != nil {
			return Status{}, err
		}
	}
	if _, err := r.DB.ExecContext(ctx, `UPDATE edge_state SET last_hash=?, last_reconciled_at=? WHERE id=1`, hash, iso(time.Now())); err != nil {
		return Status{}, err
	}
	r.refreshCerts(ctx)
	return r.Status(ctx)
}

// refreshCerts updates domains.cert_* from Caddy's storage; a certificate still missing after 10 minutes is an error.
func (r *Reconciler) refreshCerts(ctx context.Context) {
	rows, err := r.DB.QueryContext(ctx, `SELECT id, hostname, created_at FROM domains`)
	if err != nil {
		return
	}
	type d struct{ id, host, created string }
	var ds []d
	for rows.Next() {
		var x d
		if rows.Scan(&x.id, &x.host, &x.created) == nil {
			ds = append(ds, x)
		}
	}
	rows.Close()
	for _, x := range ds {
		if ci, ok := ReadCert(r.DataDir, x.host); ok && ci.NotAfter.After(time.Now()) {
			_, _ = r.DB.ExecContext(ctx, `UPDATE domains SET cert_status='issued', cert_expires_at=?, cert_error=NULL, issuer=? WHERE id=?`, iso(ci.NotAfter), ci.Issuer, x.id)
			continue
		}
		if t, err := time.Parse(time.RFC3339, x.created); err == nil && time.Since(t) > 10*time.Minute {
			_, _ = r.DB.ExecContext(ctx, `UPDATE domains SET cert_status='error', cert_error=? WHERE id=?`,
				"No certificate after 10 minutes. Check that DNS points to this host and ports 80/443 are reachable.", x.id)
		}
	}
}

// Certificates lists per-domain certificate status.
func (r *Reconciler) Certificates(ctx context.Context) ([]Cert, error) {
	rows, err := r.DB.QueryContext(ctx, `SELECT hostname, issuer, cert_status, cert_expires_at, cert_error FROM domains ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Cert{}
	for rows.Next() {
		var c Cert
		var exp, er sql.NullString
		if err := rows.Scan(&c.Hostname, &c.Issuer, &c.Status, &exp, &er); err != nil {
			return nil, err
		}
		if exp.Valid {
			c.NotAfter = &exp.String
		}
		if er.Valid {
			c.Error = &er.String
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Loop reconciles periodically (supervision + drift repair). It exits when ctx ends.
func (r *Reconciler) Loop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := r.Reconcile(ctx); err != nil {
				r.failures++
				if r.failures == 1 || r.failures%20 == 0 {
					slog.Warn("edge reconcile failed", "err", err, "consecutive", r.failures)
				}
				continue
			}
			r.failures = 0
		}
	}
}

// SetPanelDomain implements the panel-domain transition (plan 3.12): replace the single panel row atomically.
// The loopback listener and SSH-tunnel path are untouched by this change, so access cannot be cut.
func SetPanelDomain(ctx context.Context, tx *sql.Tx, id, hostname string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE kind='panel'`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO domains(id,hostname,kind,force_https,cert_status,created_at) VALUES (?,?,?,?,?,?)`,
		id, hostname, "panel", 1, "issuing", time.Now().UTC().Format(time.RFC3339))
	return err
}
