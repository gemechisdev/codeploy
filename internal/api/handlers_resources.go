package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/deploy"
)

// hasInProgress reports whether a resource has a deployment that is queued or running: the answer every control
// route checks first, because two deployments of one resource would fight over the same names.
func (s *Server) hasInProgress(ctx context.Context, resourceID string) (bool, error) {
	var one int
	err := s.DB.QueryRowContext(ctx, `SELECT 1 FROM deployments WHERE resource_id=? AND status IN (`+inProgressStates+`) LIMIT 1`, resourceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Server) listResources(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	limit, cursor := page(r)
	q := `SELECT ` + resourceCols + resourceFrom + ` WHERE 1=1`
	args := []any{}
	if v := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))); v != "" {
		q += ` AND (r.name LIKE ? OR r.slug LIKE ?)`
		args = append(args, "%"+v+"%", "%"+v+"%")
	}
	if v := r.URL.Query().Get("environment_id"); v != "" {
		q += ` AND r.environment_id=?`
		args = append(args, v)
	}
	if cursor != "" {
		q += ` AND r.id>?`
		args = append(args, cursor)
	}
	q += ` ORDER BY r.id LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(r.Context(), q, args...)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		u, err := scanResourceRows(rows)
		if err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, u.response())
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

func (s *Server) getResource(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	if u, ok := s.findResource(w, r, chi.URLParam(r, "id")); ok {
		writeJSON(w, 200, u.response())
	}
}

type sourceInput struct {
	Kind        string    `json:"kind"`
	RepoURL     *string   `json:"repo_url"`
	Branch      *string   `json:"branch"`
	BuildPath   *string   `json:"build_path"`
	Dockerfile  *string   `json:"dockerfile"`
	PathFilters *[]string `json:"path_filters"`
	DeployKeyID *string   `json:"deploy_key_id"`
}

func (b *sourceInput) row() (kind, filters string, url, branch, buildPath, dockerfile, keyID any) {
	kind = b.Kind
	if kind == "" {
		kind = "registry"
	}
	filters = `[]`
	if b.PathFilters != nil {
		out, _ := json.Marshal(*b.PathFilters)
		filters = string(out)
	}
	return kind, filters, nilStr(b.RepoURL), nilStr(b.Branch), nilStr(b.BuildPath), nilStr(b.Dockerfile), nilStr(b.DeployKeyID)
}

func nilStr(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

func (s *Server) createResource(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	var b struct {
		EnvironmentID string          `json:"environment_id"`
		Type          string          `json:"type"`
		Name          string          `json:"name"`
		Config        json.RawMessage `json:"config"`
		Source        *sourceInput    `json:"source"`
		Domain        *string         `json:"domain"`
		DeployNow     bool            `json:"deploy_now"`
	}
	if !decode(w, r, &b) {
		return
	}
	f := map[string]string{}
	if !s.environmentExists(r.Context(), b.EnvironmentID) {
		f["environment_id"] = "Choose an environment"
	}
	if !slugRe.MatchString(b.Name) {
		f["name"] = `Use lowercase letters, digits and "-" (1-40 chars, no leading or trailing "-")`
	}
	switch b.Type {
	case "app":
	case "compose":
		if !s.flagOn(r.Context(), "deploy.compose") {
			f["type"] = "Compose resources are disabled (feature flag deploy.compose)"
		}
	case "static":
		if !s.flagOn(r.Context(), "deploy.static") {
			f["type"] = "Static sites are disabled (feature flag deploy.static)"
		}
	default:
		f["type"] = "Unsupported resource type"
	}
	cfgJSON, cfgFields, err := mergeConfigInto("", b.Config, isOwner(r))
	for k, v := range cfgFields {
		f[k] = v
	}
	if err != nil {
		f["config"] = "Config must be a JSON object"
	}
	src := b.Source
	if src == nil {
		src = &sourceInput{}
	}
	cfg, _ := deploy.ParseConfig(cfgJSON)
	if cfg.Image.Mode == "build" {
		if src.Kind == "" || src.Kind == "registry" {
			f["source"] = "A Git source is required to build on the host"
		} else if src.RepoURL == nil || !gitURLRe.MatchString(*src.RepoURL) {
			f["source.repo_url"] = "Enter an SSH (git@host:org/repo.git) or HTTPS repository URL"
		} else if src.Branch == nil || *src.Branch == "" {
			f["source.branch"] = "Branch is required"
		}
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	if b.DeployNow && !p.Perms["deploy:create"] {
		writeErr(w, 403, "forbidden", "Missing permission: deploy:create", nil)
		return
	}
	domain := ""
	if b.Domain != nil && strings.TrimSpace(*b.Domain) != "" {
		h := strings.ToLower(strings.TrimSpace(*b.Domain))
		if len(h) > 253 || !hostRe.MatchString(h) {
			invalid(w, map[string]string{"hostname": "Enter a valid hostname such as app.example.com"})
			return
		}
		var kind string
		err := s.DB.QueryRowContext(r.Context(), `SELECT kind FROM domains WHERE hostname=?`, h).Scan(&kind)
		switch {
		case err == nil && kind == "panel":
			invalid(w, map[string]string{"hostname": "This hostname is reserved for the panel"})
			return
		case err == nil:
			writeErr(w, 409, "conflict", h+" is already routed to another resource", map[string]any{"hostname": h})
			return
		case !errors.Is(err, sql.ErrNoRows):
			internalErr(w, err)
			return
		}
		domain = h
	}

	id, depID, created := newID("res_"), "", now()
	var conflict, domainTaken bool
	var deployment *map[string]any
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO resources(id,environment_id,type,name,slug,config_json,desired_state,created_at) VALUES (?,?,?,?,?,?,?,?)`,
			id, b.EnvironmentID, b.Type, b.Name, b.Name, cfgJSON, "stopped", created); err != nil {
			if isUniqueErr(err) {
				conflict = true
				return audit.Event{}, sql.ErrNoRows
			}
			return audit.Event{}, err
		}
		kind, filters, url, branch, buildPath, dockerfile, keyID := src.row()
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO resource_sources(resource_id,kind,repo_url,branch,build_path,dockerfile,path_filters_json,deploy_key_id) VALUES (?,?,?,?,?,?,?,?)`,
			id, kind, url, branch, buildPath, dockerfile, filters, keyID); err != nil {
			return audit.Event{}, err
		}
		if domain != "" {
			// The resource's first domain is its primary one; the reconciler will not route to it until a
			// deployment succeeds and gives it an upstream.
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO domains(id,hostname,resource_id,kind,force_https,redirect_www,upstream,cert_status,issuer,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				newID("dom_"), domain, id, "resource", 1, 0, "", "issuing", "", created); err != nil {
				if isUniqueErr(err) {
					domainTaken = true
					return audit.Event{}, sql.ErrNoRows
				}
				return audit.Event{}, err
			}
		}
		ev, err := s.rec(r.Context(), tx, r, p, "resource.created", "resource", id,
			map[string]any{"name": b.Name, "type": b.Type, "mode": cfg.Image.Mode, "environment_id": b.EnvironmentID})
		if err != nil {
			return audit.Event{}, err
		}
		if !b.DeployNow {
			return ev, nil
		}
		ref := ""
		if cfg.Image.Mode == "pull" && cfg.Image.Ref != nil {
			ref = *cfg.Image.Ref
		}
		branchName := ""
		if src.Branch != nil {
			branchName = *src.Branch
		}
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: id, Trigger: deploy.TrManual, Mode: cfg.Image.Mode, Branch: branchName, ImageRef: ref,
		})
		if err != nil {
			return audit.Event{}, err
		}
		// Read the row back inside the transaction: the queue worker cannot change it before this commit, so what
		// the client is shown is the state the deployment was created in, not the state it reached meanwhile.
		d, err := deploymentJSON(r.Context(), tx, depID)
		if err != nil {
			return audit.Event{}, err
		}
		deployment = &d
		ev, err = s.rec(r.Context(), tx, r, p, "deployment.created", "deployment", depID,
			map[string]any{"resource": b.Name, "mode": cfg.Image.Mode, "trigger": "manual"})
		return ev, err
	})
	if conflict {
		writeErr(w, 409, "conflict", `A resource named "`+b.Name+`" already exists`, map[string]any{"name": b.Name})
		return
	}
	if domainTaken {
		writeErr(w, 409, "conflict", domain+" is already routed to another resource", map[string]any{"hostname": domain})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": id})
	if domain != "" {
		s.Bus.Publish("edge.changed", map[string]any{"domain_id": nil})
		s.reconcileAfter()
	}
	u, err := s.resourceByID(r.Context(), id)
	if err != nil {
		internalErr(w, err)
		return
	}
	resp := map[string]any{"resource": u.response(), "deployment": nil}
	if deployment != nil {
		resp["deployment"] = *deployment
	}
	writeJSON(w, 201, resp)
}

func (s *Server) environmentExists(ctx context.Context, id string) bool {
	var one int
	return s.DB.QueryRowContext(ctx, `SELECT 1 FROM environments WHERE id=?`, id).Scan(&one) == nil
}

func (s *Server) patchResource(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var b struct {
		Name   *string         `json:"name"`
		Config json.RawMessage `json:"config"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Name != nil && *b.Name != cur.Name {
		invalid(w, map[string]string{"name": "Resources cannot be renamed: container and image names embed the slug"})
		return
	}
	fields := []string{}
	cfgJSON := cur.CfgJSON
	if len(b.Config) > 0 {
		merged, vfields, err := mergeConfigInto(cur.CfgJSON, b.Config, isOwner(r))
		if err != nil {
			invalid(w, map[string]string{"config": "Config must be a JSON object"})
			return
		}
		if len(vfields) > 0 {
			invalid(w, vfields)
			return
		}
		cfgJSON = merged
		fields = append(fields, "config")
	}
	if len(fields) == 0 {
		writeJSON(w, 200, cur.response()) // a patch that changes nothing writes no audit row
		return
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `UPDATE resources SET config_json=? WHERE id=?`, cfgJSON, cur.ID); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "resource.updated", "resource", cur.ID, map[string]any{"fields": fields})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	if u, ok := s.findResource(w, r, cur.ID); ok {
		writeJSON(w, 200, u.response())
	}
}

func (s *Server) deleteResource(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	busy, err := s.hasInProgress(r.Context(), cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	if busy {
		writeErr(w, 409, "conflict", "Cancel the running deployment before deleting this resource", nil)
		return
	}
	// The image tags go to the reap job before the deployment rows that named them cascade away: deleting a
	// resource must not leave its images on the host forever, and it must not delete anyone else's either.
	images := e.ResourceTags(r.Context(), cur.ID)
	var domainIDs []string
	if rows, err := s.DB.QueryContext(r.Context(), `SELECT id FROM domains WHERE resource_id=?`, cur.ID); err == nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				domainIDs = append(domainIDs, id)
			}
		}
		rows.Close()
	}
	err = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM domains WHERE resource_id=?`, cur.ID); err != nil {
			return audit.Event{}, err
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM resources WHERE id=?`, cur.ID); err != nil {
			return audit.Event{}, err
		}
		if err := e.EnqueueReap(r.Context(), tx, s.Jobs, deploy.ReapRequest{ResourceID: cur.ID, Slug: cur.Slug, Images: images}); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "resource.deleted", "resource", cur.ID,
			map[string]any{"name": cur.Name, "domains_removed": len(domainIDs), "images_queued": len(images)})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	s.Bus.Publish("edge.changed", map[string]any{})
	s.reconcileAfter()
	w.WriteHeader(204)
}

// ---- start / stop / restart ----
//
// These three do not poke containers from the request. Stopping writes desired_state and queues the reap; starting
// and restarting enqueue a deployment that reuses the retained image. That keeps the API's answer consistent with
// what the engine alone may do to a container, and makes a daemon that dies mid-action recover through the queue.

func (s *Server) stopResource(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if busy, err := s.inProgressConflict(w, r, cur.ID); err != nil || busy {
		return
	}
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `UPDATE resources SET desired_state='stopped' WHERE id=?`, cur.ID); err != nil {
			return audit.Event{}, err
		}
		if err := e.EnqueueReap(r.Context(), tx, s.Jobs, deploy.ReapRequest{ResourceID: cur.ID, Slug: cur.Slug}); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "resource.stopped", "resource", cur.ID, map[string]any{"name": cur.Name})
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	s.reconcileAfter() // the route drops as soon as desired_state says stopped, without waiting for Docker
	if u, ok := s.findResource(w, r, cur.ID); ok {
		writeJSON(w, 200, u.response())
	}
}

// startResource brings the resource back on the version that was last active. A resource that never deployed has
// nothing retained to start, so the answer is the same as the UI's: deploy it first.
func (s *Server) startResource(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if busy, err := s.inProgressConflict(w, r, cur.ID); err != nil || busy {
		return
	}
	ref, active, hasActive := e.ActiveImageRef(r.Context(), cur.ID)
	if !hasActive {
		writeErr(w, 409, "conflict", "This resource has no successful deployment yet. Deploy it first.", nil)
		return
	}
	depID := ""
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `UPDATE resources SET desired_state='running' WHERE id=?`, cur.ID); err != nil {
			return audit.Event{}, err
		}
		var err error
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: cur.ID, Trigger: deploy.TrManual, Mode: "pull", ImageRef: ref, RollbackOf: active,
		})
		if err != nil {
			return audit.Event{}, err
		}
		if _, err := s.rec(r.Context(), tx, r, p, "resource.started", "resource", cur.ID, map[string]any{"name": cur.Name, "deployment_id": depID}); err != nil {
			return audit.Event{}, err
		}
		ev, err := s.rec(r.Context(), tx, r, p, "deployment.created", "deployment", depID,
			map[string]any{"resource": cur.Name, "mode": "pull", "trigger": "manual", "reuses": active})
		return ev, err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	s.Bus.Publish("deployment.updated", map[string]any{"deployment_id": depID, "resource_id": cur.ID})
	if u, ok := s.findResource(w, r, cur.ID); ok {
		writeJSON(w, 200, u.response())
	}
}

// restartResource redeploys the active image as a new deployment: the new container is verified before the route
// moves, so a restart is a restart of the workload without a blind gap in service.
func (s *Server) restartResource(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	if busy, err := s.inProgressConflict(w, r, cur.ID); err != nil || busy {
		return
	}
	ref, active, hasActive := e.ActiveImageRef(r.Context(), cur.ID)
	if !hasActive {
		writeErr(w, 409, "conflict", "This resource has no successful deployment yet. Deploy it first.", nil)
		return
	}
	depID := ""
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `UPDATE resources SET desired_state='running' WHERE id=?`, cur.ID); err != nil {
			return audit.Event{}, err
		}
		var err error
		depID, err = e.Create(r.Context(), tx, s.Jobs, deploy.NewDeployment{
			ResourceID: cur.ID, Trigger: deploy.TrManual, Mode: "pull", ImageRef: ref, RollbackOf: active,
		})
		if err != nil {
			return audit.Event{}, err
		}
		if _, err := s.rec(r.Context(), tx, r, p, "resource.restarted", "resource", cur.ID, map[string]any{"name": cur.Name, "deployment_id": depID}); err != nil {
			return audit.Event{}, err
		}
		ev, err := s.rec(r.Context(), tx, r, p, "deployment.created", "deployment", depID,
			map[string]any{"resource": cur.Name, "mode": "pull", "trigger": "manual", "reuses": active})
		return ev, err
	})
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("resource.updated", map[string]any{"resource_id": cur.ID})
	if u, ok := s.findResource(w, r, cur.ID); ok {
		writeJSON(w, 200, u.response())
	}
}

func (s *Server) inProgressConflict(w http.ResponseWriter, r *http.Request, resourceID string) (bool, error) {
	busy, err := s.hasInProgress(r.Context(), resourceID)
	if err != nil {
		internalErr(w, err)
		return true, err
	}
	if busy {
		writeErr(w, 409, "conflict", "A deployment is in progress", nil)
		return true, nil
	}
	return false, nil
}

func (s *Server) resourcePolicy(w http.ResponseWriter, r *http.Request) {
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	res, err := e.Policy(r.Context(), cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}
