package deploy

import (
	"context"
	"errors"
	"log/slog"

	"github.com/codeplo/codeplo/internal/modules/runtime"
)

// retentionCount is the resource's own retain_images, falling back to the daemon setting and then to the plan's
// default of five.
func (e *Engine) retentionCount(ctx context.Context, resourceID string) int {
	var raw string
	if err := e.db.QueryRowContext(ctx, `SELECT config_json FROM resources WHERE id=?`, resourceID).Scan(&raw); err == nil {
		if c, cerr := ParseConfig(raw); cerr == nil && c.RetainImages > 0 {
			return c.RetainImages
		}
	}
	if v := e.settingInt(ctx, "retain_images", 5); v > 0 {
		return v
	}
	return 5
}

// EnforceRetention is the reaper as the API sees it: after a resource is stopped or a deployment is deleted, the
// images that fell out of the retention window go with it.
func (e *Engine) EnforceRetention(ctx context.Context, resourceID string) error {
	var slug, active string
	if err := e.db.QueryRowContext(ctx, `SELECT slug, COALESCE(active_deployment_id,'') FROM resources WHERE id=?`, resourceID).Scan(&slug, &active); err != nil {
		return err
	}
	e.enforceRetention(ctx, resourceID, slug, active)
	return nil
}

// enforceRetention keeps the last retain_images *digests* of a resource (plan: rollback redeploys a retained
// digest, never rebuilds) and untags what fell out.
//
// Two rules keep it from destroying a live deployment. Only terminal deployments are considered, so the one still
// observing is never touched. And removal is by tag (codeplo/<slug>:<deployment id>) - Docker keeps the layers of a
// digest another tag still points at, which is exactly what a rollback deployment shares with its source.
func (e *Engine) enforceRetention(ctx context.Context, resourceID, slug, activeDepID string) {
	keep := e.retentionCount(ctx, resourceID)
	rows, err := e.db.QueryContext(ctx, `SELECT id, COALESCE(image_digest,'') FROM deployments
		WHERE resource_id=? AND image_digest IS NOT NULL AND image_digest<>''
		  AND status IN ('succeeded','rolled_back','failed','cancelled','interrupted')
		ORDER BY started_at DESC, id DESC`, resourceID)
	if err != nil {
		slog.Warn("deploy: retention pass could not read the deployment list", "resource", resourceID, "err", err)
		return
	}
	defer rows.Close()

	type row struct{ id, digest string }
	var kept, dropped []row
	seen := map[string]bool{}
	for rows.Next() {
		var d row
		if err := rows.Scan(&d.id, &d.digest); err != nil {
			slog.Warn("deploy: retention pass skipped a deployment", "err", err)
			continue
		}
		switch {
		case seen[d.digest]:
			kept = append(kept, d) // shares a digest with something already kept: no extra budget, nothing to remove
		case len(seen) < keep:
			seen[d.digest] = true
			kept = append(kept, d)
		default:
			dropped = append(dropped, d)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Warn("deploy: retention pass stopped early", "resource", resourceID, "err", err)
		return
	}

	mark := func(retained int, list []row) error {
		for _, d := range list {
			if _, err := e.db.ExecContext(ctx, `UPDATE deployments SET retained=? WHERE id=? AND status NOT IN ('preparing','building','starting','verifying','switching','observing','queued')`,
				retained, d.id); err != nil {
				return err
			}
		}
		return nil
	}
	if err := mark(1, kept); err != nil {
		slog.Warn("deploy: retention could not mark the kept images", "resource", resourceID, "err", err)
		return
	}
	if err := mark(0, dropped); err != nil {
		slog.Warn("deploy: retention could not mark the expired images", "resource", resourceID, "err", err)
		return
	}

	for _, d := range dropped {
		if d.id == activeDepID {
			continue // never untag what the route is using
		}
		ref := "codeplo/" + slug + ":" + d.id
		if err := e.rt.RemoveImage(ctx, ref); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			slog.Warn("deploy: retention could not remove an image", "image", ref, "err", err)
		}
	}
	if n := len(dropped); n > 0 {
		e.pub("resource.updated", map[string]any{"resource_id": resourceID})
		slog.Info("deploy: retention removed expired images", "resource", resourceID, "count", n, "keep", keep)
	}
}
