package deploy

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/store"
)

// Recover runs at boot, before the queue starts (plan 3.8). It closes the gap a crash leaves open: a deployment the
// daemon was mid-way through can never be resumed, so it becomes `interrupted`, and the containers it left behind
// are removed by label rather than by anything the crashed process remembered.
//
// Queued deployments are deliberately not interrupted: the queue re-runs jobs it found 'running' and the handler is
// idempotent (Run is a no-op unless the row is still 'queued'), so a queued deployment simply starts.
//
// A container belonging to a resource's *active* deployment is never removed, even when that deployment is being
// marked interrupted: the crash can have happened after the switch, and killing the container that is serving live
// traffic would turn a stalled deployment into an outage.
func (e *Engine) Recover(ctx context.Context) error {
	n, err := e.interrupt(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		slog.Warn("deploy: marked deployments interrupted after a crash or restart", "count", n)
	}
	if err := e.removeOrphans(ctx); err != nil {
		return err
	}
	return e.refreshUpstreams(ctx)
}

// refreshUpstreams re-reads the address of every running resource's active container.
//
// A deployment records its container's IP when the container starts, but Docker renumbers addresses when the daemon
// restarts -- and after a host reboot the surviving container is exactly the one serving live traffic. Left alone,
// the edge would keep routing to an address that no longer belongs to it, so recovery repairs the pointer's
// address before the first reconcile rather than waiting for a new deployment to fix it.
func (e *Engine) refreshUpstreams(ctx context.Context) error {
	rows, err := e.db.QueryContext(ctx, `SELECT r.id, r.environment_id, d.id, d.container_id, d.upstream, r.config_json
		FROM resources r JOIN deployments d ON d.id=r.active_deployment_id
		WHERE r.desired_state='running' AND COALESCE(d.container_id,'')<>''`)
	if err != nil {
		return err
	}
	type target struct {
		resID, depID, cid, was, cfg string
		network                     string
	}
	var list []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.resID, &t.network, &t.depID, &t.cid, &t.was, &t.cfg); err != nil {
			rows.Close()
			return err
		}
		t.network = "cpl-env-" + t.network
		list = append(list, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range list {
		st, err := e.rt.InspectContainer(ctx, t.cid, t.network)
		if err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				continue // the sweep or a manual `docker rm` already took it; nothing has an address to refresh
			}
			if errors.Is(err, runtime.ErrUnavailable) {
				return nil // Docker is not up yet; the next boot or deployment records the address
			}
			slog.Warn("deploy: could not inspect the active container", "container", shortID(t.cid), "err", err)
			continue
		}
		if !st.Running || st.IP == "" {
			continue
		}
		rc, err := ParseConfig(t.cfg)
		if err != nil || rc.Port <= 0 {
			continue
		}
		up := st.IP + ":" + strconv.Itoa(rc.Port)
		if up == t.was {
			continue
		}
		if err := e.setUpstream(ctx, t.depID, t.resID, up); err != nil {
			slog.Warn("deploy: could not refresh the routed address", "deployment", t.depID, "err", err)
			continue
		}
		slog.Info("deploy: refreshed the routed address after a runtime restart", "deployment", t.depID, "from", t.was, "to", up)
	}
	return nil
}

// setUpstream writes the address in the same statement pair the switch step uses, so a crash between them cannot
// leave the domain's cached copy pointing at a container that has already moved.
func (e *Engine) setUpstream(ctx context.Context, depID, resID, up string) error {
	return store.InTx(ctx, e.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET upstream=? WHERE id=?`, up, depID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE domains SET upstream=? WHERE resource_id=?`, up, resID)
		return err
	})
}

func (e *Engine) interrupt(ctx context.Context) (int, error) {
	type dep struct{ id, resourceID string }
	var list []dep
	rows, err := e.db.QueryContext(ctx, `SELECT id, resource_id FROM deployments WHERE status IN (`+runningStates+`)`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var d dep
		if err := rows.Scan(&d.id, &d.resourceID); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	rows.Close()

	for _, d := range list {
		err := store.InTx(ctx, e.db, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE deployments SET status='interrupted', step='interrupted', error=?, finished_at=?, heartbeat_at=?, observe_until=NULL
				WHERE id=? AND status IN (`+runningStates+`)`,
				"The daemon stopped while this deployment was running. Nothing was resumed: redeploy the resource.", ts(e.cfg.Now()), ts(e.cfg.Now()), d.id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil // finished on another path in the meantime
			}
			_, err = audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "deploy-engine", Action: "deployment.interrupted",
				TargetType: "deployment", TargetID: d.id, Meta: map[string]any{"resource_id": d.resourceID, "status": StInterrupted}})
			return err
		})
		if err != nil {
			slog.Warn("deploy: could not mark a deployment interrupted", "deployment", d.id, "err", err)
			continue
		}
		e.emit(d.id)
	}
	return len(list), nil
}

// removeOrphans kills and removes every codeplo.deployment container whose deployment is not the active one of its
// resource. Label-driven, so it needs no state from before the crash, and idempotent: running it twice is a no-op.
func (e *Engine) removeOrphans(ctx context.Context) error {
	cs, err := e.rt.ListContainers(ctx, runtime.LabelDeployment)
	if err != nil {
		// Docker may simply not be up yet at boot; the supervisor and the next deployment's cleanup both recover from this.
		if errors.Is(err, runtime.ErrUnavailable) {
			slog.Warn("deploy: orphan sweep skipped, Docker is not reachable yet", "err", err)
			return nil
		}
		return err
	}
	for _, c := range cs {
		depID := c.Labels[runtime.LabelDeployment]
		resID := c.Labels[runtime.LabelResource]
		if depID == "" {
			continue
		}
		if resID == "" {
			// The label that says what it belonged to is missing, so nothing can prove it is not serving.
			slog.Warn("deploy: orphan sweep left an unlabelled container alone", "container", shortID(c.ID), "deployment", depID)
			continue
		}
		var active string
		if err := e.db.QueryRowContext(ctx, `SELECT COALESCE(active_deployment_id,'') FROM resources WHERE id=?`, resID).Scan(&active); err != nil {
			slog.Warn("deploy: orphan sweep could not read the resource", "resource", resID, "err", err)
			continue
		}
		if active == depID {
			continue // this container is serving traffic
		}
		_ = e.rt.KillContainer(ctx, c.ID)
		if err := e.rt.RemoveContainer(ctx, c.ID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			slog.Warn("deploy: orphan sweep could not remove a container", "container", c.ID, "deployment", depID, "err", err)
			continue
		}
		slog.Info("deploy: removed an orphaned container", "container", shortID(c.ID), "deployment", depID)
		e.emit(depID)
	}
	return nil
}
