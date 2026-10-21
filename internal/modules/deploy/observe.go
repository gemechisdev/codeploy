package deploy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/store"
)

// observe is the post-switch safety window (plan 3.8, F17). Traffic has already moved to the new container and the
// previous one is still running: the window watches the new version and, if it fails FailThreshold consecutive
// checks, moves the route back and retires the bad container instead of leaving users on a broken version.
//
// It runs inside the queue job on purpose. The queue has one worker, which is also what serialises deployments of
// a resource: no second deployment can be switching or observing while this window is open. The price is that a
// window holds the build slot for its length (observation_window_s, 10-600 s, default 60) - see docs/DECISIONS.md.
//
// After the switch a plain failure is never an option: this deployment owns the route, so `finish` must not mark it
// failed and clean up the container that is serving live traffic. Every outcome is one of four transitions -
// succeeded, rolled back, "already recorded, write nothing", or cancellation escaping to boot recovery.
func (r *run) observe(ctx context.Context) error {
	e := r.e
	hc := r.res.Cfg.Healthcheck

	window := e.cfg.Observe
	if window <= 0 {
		window = time.Duration(e.settingInt(ctx, "observation_window_s", 60)) * time.Second
	}
	if window < time.Second {
		window = time.Second
	}
	every := e.cfg.ObserveEvery
	if every <= 0 {
		every = time.Duration(hc.IntervalS) * time.Second
	}
	if every > 5*time.Second {
		every = 5 * time.Second // the window stays responsive even with a lazily configured health check
	}
	if every < time.Second {
		every = time.Second
	}
	url := "http://" + r.upstream + hc.Path
	probeTimeout := time.Duration(hc.TimeoutS) * time.Second

	if err := r.setStep(ctx, StObserving, "observe"); err != nil {
		return err
	}
	until := e.cfg.Now().Add(window)
	if _, err := e.db.ExecContext(ctx, `UPDATE deployments SET observe_until=? WHERE id=? AND status='observing'`, ts(until), r.id); err != nil {
		return err
	}
	if r.prev.ID != "" {
		r.logw.Sys(fmt.Sprintf("observing for %s: %d consecutive failed checks roll the route back to %s", window, e.cfg.FailThreshold, r.prev.ID))
	} else {
		r.logw.Sys(fmt.Sprintf("observing for %s: %d consecutive failed checks end this deployment (there is no previous version to fall back to)", window, e.cfg.FailThreshold))
	}

	tailed := false
	fails := 0
	for {
		ok, status, gone := r.check(ctx, url, probeTimeout, &tailed)
		if ctx.Err() != nil {
			return r.stopObserving(ctx, ctx.Err())
		}
		if ok {
			fails = 0
		} else {
			fails++
			r.logw.Sys(fmt.Sprintf("observation check %d/%d failed: %s", fails, e.cfg.FailThreshold, status))
		}
		// health_done stays at the verify step's attempt count (the UI reads it against health_max); the window
		// reports its latest result and keeps the heartbeat fresh.
		if _, err := e.db.ExecContext(ctx, `UPDATE deployments SET health_last=?, heartbeat_at=? WHERE id=?`,
			status, ts(e.cfg.Now()), r.id); err != nil {
			r.logw.Sys("could not record the observation result: " + err.Error())
		}
		if !ok && (gone || fails >= e.cfg.FailThreshold) {
			return r.rollback(ctx, fmt.Sprintf("%d consecutive failed checks during the %s observation window (%s)", fails, window, status))
		}
		if !e.cfg.Now().Before(until) {
			return r.succeed(ctx)
		}
		if err := e.cfg.Sleep(ctx, every); err != nil {
			return r.stopObserving(ctx, err)
		}
	}
}

// check is one observation: is the container still up, and does the health endpoint answer? `gone` means the
// container has exited or vanished, which no failure threshold should wait for.
func (r *run) check(ctx context.Context, url string, probeTimeout time.Duration, tailed *bool) (ok bool, status string, gone bool) {
	e := r.e
	st, err := e.rt.InspectContainer(ctx, r.newCID, r.network)
	if err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			return false, "the container no longer exists", true
		}
		return false, "the container status is unreadable: " + err.Error(), false
	}
	if !st.Running {
		if !*tailed {
			*tailed = true
			r.tailLogs()
		}
		return false, "the container " + r.exitReason(st), true
	}
	if err := e.cfg.Probe(ctx, url, probeTimeout); err != nil {
		return false, err.Error(), false
	}
	return true, "200 OK", false
}

// stopObserving handles a cancelled window. Cancellation cannot undo a switch, so a user cancel finalizes the
// deployment as if the window had passed; a daemon shutdown leaves the row in 'observing' for boot recovery.
func (r *run) stopObserving(ctx context.Context, err error) error {
	if !errors.Is(context.Cause(ctx), errCancelled) {
		return err // shutting down: recovery marks the deployment interrupted, and its container keeps serving
	}
	r.logw.Sys("cancellation cannot undo a switch: the observation window ended early and this deployment keeps serving")
	return r.succeed(context.WithoutCancel(ctx))
}

// succeed closes the window: the new version is final, the old container is retired, retention runs.
func (r *run) succeed(ctx context.Context) error {
	e := r.e
	err := store.InTx(ctx, e.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE deployments SET status='succeeded', step='succeeded', finished_at=?, heartbeat_at=?, observe_until=NULL, retained=1 WHERE id=? AND status='observing'`,
			ts(e.cfg.Now()), ts(e.cfg.Now()), r.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("deployment is no longer observing")
		}
		_, err = audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "deploy-engine", Action: "deployment.succeeded",
			TargetType: "deployment", TargetID: r.id, Meta: map[string]any{"resource_id": r.dep.ResourceID, "status": StSucceeded}})
		return err
	})
	if err != nil {
		// The window passed but the outcome could not be recorded. The deployment is serving and its row still says
		// 'observing'; report that instead of letting finish write a 'failed' state the engine never observed.
		r.logw.Sys("could not record the passed observation window: " + err.Error() + " - " + r.id + " keeps serving")
		return errSettled
	}
	r.logw.Sys("observation window passed; the previous container is retired and this image is retained by digest")
	r.retirePrevious(ctx)
	e.emit(r.id)
	e.enforceRetention(context.WithoutCancel(ctx), r.res.ID, r.res.Slug, r.id)
	return nil
}

// retirePrevious stops the container the switch left behind, then removes it. Best effort, and never this
// deployment's own container.
func (r *run) retirePrevious(ctx context.Context) {
	e := r.e
	if r.prev.ID == "" || r.prev.ID == r.id || r.prev.ContainerID == "" {
		return
	}
	bg, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	r.logw.Sys("stopping the previous container " + shortID(r.prev.ContainerID) + " (deployment " + r.prev.ID + ")")
	if err := e.rt.StopContainer(bg, r.prev.ContainerID, e.cfg.StopGraceS); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		r.logw.Sys("could not stop the previous container: " + err.Error())
	}
	if err := e.rt.RemoveContainer(bg, r.prev.ContainerID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		r.logw.Sys("could not remove the previous container: " + err.Error())
	}
}

// rollback moves the route back to the previous deployment and retires this one's container. Besides the switch
// step, this is the only place that writes resources.active_deployment_id.
func (r *run) rollback(ctx context.Context, reason string) error {
	e := r.e
	bg := context.WithoutCancel(ctx)
	prevID := r.prev.ID

	err := store.InTx(bg, e.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(bg, `UPDATE deployments SET status='rolled_back', step='rolled_back', error=?, rollback_reason=?, finished_at=?, observe_until=NULL, retained=0 WHERE id=? AND status='observing'`,
			"Rolled back automatically", reason, ts(e.cfg.Now()), r.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("deployment is no longer observing")
		}
		desired := "running"
		if prevID == "" {
			desired = "stopped" // nothing serves this resource now: do not leave a running intent with no container behind it
		}
		if _, err := tx.ExecContext(bg, `UPDATE resources SET active_deployment_id=?, desired_state=?, started_at=? WHERE id=?`,
			nullable(prevID), desired, ts(e.cfg.Now()), r.res.ID); err != nil {
			return err
		}
		_, err = audit.Append(bg, tx, audit.Entry{ActorType: "system", ActorID: "deploy-engine", Action: "deployment.rolled_back",
			TargetType: "deployment", TargetID: r.id, Meta: map[string]any{"resource_id": r.dep.ResourceID, "reason": reason, "restored": prevID}})
		return err
	})
	if err != nil {
		// The route was not moved. Keep serving and say so loudly: reporting a rollback the database does not hold
		// would leave Caddy and the panel disagreeing about who serves the domain.
		r.logw.Sys("AUTOMATIC ROLLBACK COULD NOT BE RECORDED: " + err.Error() + " - " + r.id + " is still serving traffic")
		return errSettled
	}

	r.logw.Sys("automatic rollback: " + reason)
	if prevID != "" {
		r.restorePrevious(bg)
	} else {
		r.logw.Sys("there was no previous deployment to restore: the resource is stopped and its domains are unrouted")
	}
	r.retireSelf(bg)
	e.syncEdge(bg)
	e.emit(r.id)
	e.enforceRetention(bg, r.res.ID, r.res.Slug, prevID)
	return errRolledBack
}

// restorePrevious makes the old container serve again. A stop_first rollout had stopped it; a start_first one left
// it running and starting a running container is a no-op the daemon answers without harm.
func (r *run) restorePrevious(ctx context.Context) {
	e := r.e
	if r.prev.ContainerID == "" {
		r.logw.Sys("the previous deployment has no recorded container; redeploy or roll back from the deployment list")
		return
	}
	st, err := e.rt.InspectContainer(ctx, r.prev.ContainerID, r.network)
	if errors.Is(err, runtime.ErrNotFound) {
		r.logw.Sys("the previous container no longer exists, so traffic could not be restored automatically; redeploy the resource")
		return
	}
	if err != nil {
		r.logw.Sys("the previous container's state is unreadable: " + err.Error())
		return
	}
	if st.Running {
		r.logw.Sys("previous container " + shortID(r.prev.ContainerID) + " is still running and still serves the route")
		return
	}
	if err := e.rt.StartContainer(ctx, r.prev.ContainerID); err != nil {
		r.logw.Sys("could not restart the previous container: " + err.Error())
		return
	}
	r.logw.Sys("previous container " + shortID(r.prev.ContainerID) + " restarted; traffic is restored")
}

// retireSelf removes the failed deployment's own container. Its image stays: retention decides when it goes.
func (r *run) retireSelf(ctx context.Context) {
	e := r.e
	if r.newCID == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	r.logw.Sys("retiring the rolled-back container " + shortID(r.newCID))
	_ = e.rt.KillContainer(cctx, r.newCID)
	if err := e.rt.RemoveContainer(cctx, r.newCID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
		r.logw.Sys("could not remove the rolled-back container: " + err.Error())
	}
}
