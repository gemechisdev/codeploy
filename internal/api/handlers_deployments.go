package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/runtime"
)

func (s *Server) deploymentRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/resources/{id}/deployments", Perm: "resource:read", Module: "deploy", Handler: s.listResourceDeployments},
		{Method: "POST", Path: "/resources/{id}/deployments", Perm: "deploy:create", Module: "deploy", Handler: s.createDeployment},
		{Method: "GET", Path: "/deployments/{id}", Perm: "resource:read", Module: "deploy", Handler: s.getDeployment},
		{Method: "GET", Path: "/deployments/{id}/logs", Perm: "resource:read", Module: "deploy", Handler: s.deploymentLogs},
		{Method: "POST", Path: "/deployments/{id}/cancel", Perm: "deploy:cancel", Module: "deploy", Handler: s.cancelDeployment},
		{Method: "POST", Path: "/deployments/{id}/rollback", Perm: "deploy:rollback", Module: "deploy", Handler: s.rollbackDeployment},
		{Method: "POST", Path: "/system/cleanup", Perm: "system:cleanup", Module: "deploy", Handler: s.systemCleanup},
	}
}

const deploymentCols = `id, resource_id, status, step, trigger, commit_sha, commit_message, branch, image_ref, image_digest,
 started_at, finished_at, heartbeat_at, observe_until, rollback_of, error, rollback_reason, preflight_json,
 health_done, health_max, health_last, retained`

// deploymentJSON renders one deployments row. It reads through a rowQueryer so a create response can show the row
// as the transaction wrote it: the queue worker takes the deployment the instant the tx commits, and a client that
// polled afterwards would otherwise see a state that was never the one it asked for.
func deploymentJSON(ctx context.Context, q rowQueryer, id string) (map[string]any, error) {
	var (
		dID, resID, status, step, trigger, imageRef, startedAt       string
		sha, msg, branch, digest, fin, hb, obs, rb, errTxt, rr, last sql.NullString
		pref                                                         sql.NullString
		done, mx, retained                                           int
	)
	err := q.QueryRowContext(ctx, `SELECT `+deploymentCols+` FROM deployments WHERE id=?`, id).
		Scan(&dID, &resID, &status, &step, &trigger, &sha, &msg, &branch, &imageRef, &digest,
			&startedAt, &fin, &hb, &obs, &rb, &errTxt, &rr, &pref, &done, &mx, &last, &retained)
	if err != nil {
		return nil, err
	}
	nul := func(s sql.NullString) any {
		if s.Valid {
			return s.String
		}
		return nil
	}
	var pre any
	if pref.Valid && pref.String != "" {
		pre = json.RawMessage(pref.String)
	}
	return map[string]any{
		"id": dID, "resource_id": resID, "status": status, "step": step, "trigger": trigger,
		"commit_sha": nul(sha), "commit_message": nul(msg), "branch": nul(branch),
		"image_ref": imageRef, "image_digest": nul(digest),
		"started_at": startedAt, "finished_at": nul(fin), "heartbeat_at": nul(hb), "observe_until": nul(obs),
		"rollback_of": nul(rb), "error": nul(errTxt), "rollback_reason": nul(rr), "preflight": pre,
		"health_attempts": map[string]any{"done": done, "max": mx, "last_status": nul(last)},
		"retained":        retained == 1, "server_now": now(),
	}, nil
}

// depStatus reads only a deployment's status and resource id, for the checks that act before loading the row.
func depStatus(ctx context.Context, q rowQueryer, id string) (status, resourceID string, err error) {
	err = q.QueryRowContext(ctx, `SELECT status, resource_id FROM deployments WHERE id=?`, id).Scan(&status, &resourceID)
	return
}

func (s *Server) listResourceDeployments(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	rid := chi.URLParam(r, "id")
	if _, ok := s.findResource(w, r, rid); !ok {
		return
	}
	limit, cursor := page(r)
	q := `SELECT ` + deploymentCols + ` FROM deployments WHERE resource_id=?`
	args := []any{rid}
	if cursor != "" {
		q += ` AND id<?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(r.Context(), q, args...)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, d)
	}
	if err := rows.Err(); err != nil {
		internalErr(w, err)
		return
	}
	var next any
	if len(items) > limit {
		next = items[limit-1]["id"]
		items = items[:limit]
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}

// scanDeployment is the row-form of deploymentJSON: the same field names, one item of a page.
func scanDeployment(rs *sql.Rows) (map[string]any, error) {
	var (
		dID, resID, status, step, trigger, imageRef, startedAt       string
		sha, msg, branch, digest, fin, hb, obs, rb, errTxt, rr, last sql.NullString
		pref                                                         sql.NullString
		done, mx, retained                                           int
	)
	if err := rs.Scan(&dID, &resID, &status, &step, &trigger, &sha, &msg, &branch, &imageRef, &digest,
		&startedAt, &fin, &hb, &obs, &rb, &errTxt, &rr, &pref, &done, &mx, &last, &retained); err != nil {
		return nil, err
	}
	nul := func(s sql.NullString) any {
		if s.Valid {
			return s.String
		}
		return nil
	}
	var pre any
	if pref.Valid && pref.String != "" {
		pre = json.RawMessage(pref.String)
	}
	return map[string]any{
		"id": dID, "resource_id": resID, "status": status, "step": step, "trigger": trigger,
		"commit_sha": nul(sha), "commit_message": nul(msg), "branch": nul(branch),
		"image_ref": imageRef, "image_digest": nul(digest),
		"started_at": startedAt, "finished_at": nul(fin), "heartbeat_at": nul(hb), "observe_until": nul(obs),
		"rollback_of": nul(rb), "error": nul(errTxt), "rollback_reason": nul(rr), "preflight": pre,
		"health_attempts": map[string]any{"done": done, "max": mx, "last_status": nul(last)},
		"retained":        retained == 1, "server_now": now(),
	}, nil
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	d, err := deploymentJSON(r.Context(), s.DB, chi.URLParam(r, "id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "deployment not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var b struct {
		Mode     *string `json:"mode"`
		ImageRef *string `json:"image_ref"`
		Branch   *string `json:"branch"`
	}
	if !decode(w, r, &b) {
		return
	}
	if busy, err := s.hasInProgress(r.Context(), cur.ID); err != nil {
		internalErr(w, err)
		return
	} else if busy {
		writeErr(w, 409, "conflict", "A deployment is already in progress for this resource", nil)
		return
	}
	cfg, err := deploy.ParseConfig(cur.CfgJSON)
	if err != nil {
		internalErr(w, err)
		return
	}
	mode := cfg.Image.Mode
	if b.Mode != nil && *b.Mode != "" {
		mode = *b.Mode
	}
	f := map[string]string{}
	imageRef := ""
	branch := ""
	if b.Branch != nil {
		branch = *b.Branch
	}
	switch mode {
	case "pull":
		ref := ""
		if cfg.Image.Ref != nil {
			ref = *cfg.Image.Ref
		}
		if b.ImageRef != nil && *b.ImageRef != "" {
			ref = *b.ImageRef
		}
		if !deploy.ValidImageRef(ref) {
			f["image_ref"] = "Enter an image reference such as ghcr.io/org/app:tag"
		}
		imageRef = ref
	case "build":
		var srcBranch sql.NullString
		kind := ""
		err := s.DB.QueryRowContext(r.Context(), `SELECT kind, branch FROM resource_sources WHERE resource_id=?`, cur.ID).Scan(&kind, &srcBranch)
		if errors.Is(err, sql.ErrNoRows) || kind == "registry" {
			f["mode"] = "Configure a Git source before building on the host"
		}
		if branch == "" {
			branch = srcBranch.String // deploy what the source is configured to build, not the repository default
		}
	default:
		f["mode"] = "Mode must be pull or build"
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	// The same policy the engine enforces at preparing, checked before the deployment is created: the panel gets
	// the violations now instead of a failed deployment in a minute.
	if violations, err := e.CheckPolicy(r.Context(), cur.ID); err != nil {
		internalErr(w, err)
		return
	} else if len(violations) > 0 {
		writeErr(w, 409, "conflict", "Isolation policy rejected this resource", map[string]any{"violations": violations})
		return
	}
	depID := ""
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var err error
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: cur.ID, Trigger: deploy.TrManual, Mode: mode, Branch: branch, ImageRef: imageRef,
		})
		if err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "deployment.created", "deployment", depID,
			map[string]any{"resource": cur.Name, "mode": mode, "trigger": "manual", "image_ref": imageRef})
	})
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	d, err := deploymentJSON(r.Context(), s.DB, depID)
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	s.Bus.Publish("deployment.updated", map[string]any{"deployment_id": depID, "resource_id": cur.ID})
	writeJSON(w, 202, d)
}

func (s *Server) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	id := chi.URLParam(r, "id")
	status, _, err := depStatus(r.Context(), s.DB, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "deployment not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	if !deploy.Cancellable(status) {
		writeErr(w, 409, "conflict", fmt.Sprintf("A deployment that is %s cannot be cancelled", status), nil)
		return
	}
	if _, err := e.Cancel(r.Context(), id); err != nil {
		internalErr(w, err)
		return
	}
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "deployment.cancelled", "deployment", id, map[string]any{"from_status": status})
	})
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	d, err := deploymentJSON(r.Context(), s.DB, id)
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("deployment.updated", d)
	writeJSON(w, 200, d)
}

// rollbackDeployment re-points the resource at a retained image by creating a new deployment that reuses it. It is
// deliberately not a step-up action (plan 3.5): after a bad release the fastest safe path back is the one an
// operator should not have to unlock twice. The acknowledgement is what makes the database caveat explicit.
func (s *Server) rollbackDeployment(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	id := chi.URLParam(r, "id")
	var b struct {
		AcknowledgeDBUnchanged bool `json:"acknowledge_db_unchanged"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !b.AcknowledgeDBUnchanged {
		invalid(w, map[string]string{"acknowledge_db_unchanged": "Acknowledge that application rollback does not roll back database changes"})
		return
	}
	var (
		status, resID, imageRef string
		digest, active          sql.NullString
		retained                int
	)
	err := s.DB.QueryRowContext(r.Context(), `SELECT status, resource_id, image_ref, image_digest, retained FROM deployments WHERE id=?`, id).
		Scan(&status, &resID, &imageRef, &digest, &retained)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "deployment not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	cur, ok := s.findResource(w, r, resID)
	if !ok {
		return
	}
	if status != deploy.StSucceeded || !digest.Valid || digest.String == "" || retained != 1 {
		writeErr(w, 409, "conflict", "Only a succeeded deployment that still retains its image can be rolled back to", nil)
		return
	}
	if err := s.DB.QueryRowContext(r.Context(), `SELECT active_deployment_id FROM resources WHERE id=?`, resID).Scan(&active); err != nil {
		internalErr(w, err)
		return
	}
	if active.String == id {
		writeErr(w, 409, "conflict", "This deployment is already the active one", nil)
		return
	}
	if busy, err := s.hasInProgress(r.Context(), resID); err != nil {
		internalErr(w, err)
		return
	} else if busy {
		writeErr(w, 409, "conflict", "A deployment is already in progress for this resource", nil)
		return
	}
	// The retained image is addressed by the local tag the engine created for it, never by the original registry
	// reference: rolling back must reproduce the bytes that ran, not whatever the tag points at today.
	tag := deploy.ImageTag(cur.Slug, id)
	depID := ""
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var err error
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: resID, Trigger: deploy.TrRollback, Mode: "pull", ImageRef: tag, RollbackOf: id,
		})
		if err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "deployment.rollback", "deployment", depID,
			map[string]any{"to": id, "digest": digest.String, "resource": cur.Name})
	})
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	d, err := deploymentJSON(r.Context(), s.DB, depID)
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": resID})
	s.Bus.Publish("deployment.updated", map[string]any{"deployment_id": depID, "resource_id": resID})
	writeJSON(w, 202, d)
}

// POST /deployments/{id}/logs: server-sent events, one stored deployment log line per message, as the JSON object
// the contract declares (web/src/api/hooks/logs.ts parses it). History first, then follow, then `event: end` once
// the deployment is terminal so the browser stops reconnecting.
//
// The follow loop polls rather than relying only on the bus: bus delivery is deliberately lossy (a slow subscriber
// is dropped rather than blocking the engine), and losing a build log line in the UI is not acceptable. The bus
// wakes the poll early when it does deliver.
func (s *Server) deploymentLogs(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "internal", "streaming unsupported", nil)
		return
	}
	id := chi.URLParam(r, "id")
	var status string
	err := s.DB.QueryRowContext(r.Context(), `SELECT status FROM deployments WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "deployment not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	after := 0
	if v := r.URL.Query().Get("after"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			invalid(w, map[string]string{"after": "after must be a non-negative log sequence number"})
			return
		}
		after = n
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	last := after
	send := func() error {
		rows, err := s.DB.QueryContext(r.Context(), `SELECT seq, at, stream, line FROM deployment_logs WHERE deployment_id=? AND seq>? ORDER BY seq`, id, last)
		if err != nil {
			return err
		}
		defer rows.Close()
		wrote := false
		for rows.Next() {
			var seq int
			var at, strm, line string
			if err := rows.Scan(&seq, &at, &strm, &line); err != nil {
				return err
			}
			body, err := json.Marshal(map[string]any{"seq": seq, "at": at, "stream": strm, "line": line})
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", seq, body)
			last = seq
			wrote = true
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if wrote {
			fl.Flush()
		}
		return nil
	}

	ch, cancel := s.Bus.Subscribe()
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		if err := send(); err != nil {
			return // the client is gone or the row read failed; the browser will reconnect
		}
		if deploy.Terminal(status) {
			fmt.Fprint(w, "event: end\ndata: \n\n")
			fl.Flush()
			return
		}
		// Wait for something worth doing. An unrelated bus event must not wake this stream: every open log tab
		// would otherwise re-query on every log line of every deployment.
		wake := false
		for !wake {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				wake = true
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
				wake = true
			case ev := <-ch:
				if m, ok := ev.Data.(map[string]any); ok && ev.Name == "deployment.log" && m["deployment_id"] == id {
					wake = true
				}
			}
		}
		if st, _, err := depStatus(r.Context(), s.DB, id); err == nil {
			status = st
		}
	}
}

// systemCleanup reclaims what the panel itself made reclaimable: it first drops the images no longer retained by a
// resource's own retain_images budget, then asks the runtime to prune dangling images, stopped managed containers
// and the build cache. Volume data is never in scope; a resource's files survive cleanup.
func (s *Server) systemCleanup(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id FROM resources ORDER BY id`)
	if err != nil {
		internalErr(w, err)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if err := e.EnforceRetention(r.Context(), id); err != nil {
			slog.Warn("retention pass skipped", "resource_id", id, "err", err)
		}
	}
	stats, err := e.Cleanup(r.Context())
	if err != nil {
		if errors.Is(err, runtime.ErrUnavailable) {
			writeErr(w, 500, "internal", "The container runtime is not available", nil)
			return
		}
		internalErr(w, err)
		return
	}
	txErr := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "system.cleanup", "system", "docker",
			map[string]any{"reclaimed_mb": stats.ReclaimedMB, "images_removed": stats.ImagesRemoved, "containers_removed": stats.ContainersRemoved})
	})
	if txErr != nil {
		internalErr(w, txErr)
		return
	}
	writeJSON(w, 200, stats)
}
