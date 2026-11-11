// Package api: Phase 4 workload routes. This file holds the shared resource/project/environment plumbing;
// handlers_resources.go has the resource CRUD and controls, handlers_deployments.go the deployment routes.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/platform"
)

// slugRe is the resource and environment name rule (web/src/mock/util.ts). A resource slug is embedded in
// container, image and volume names, so it is immutable after creation and globally unique.
var slugRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

var gitURLRe = regexp.MustCompile(`^(git@[\w.-]+:[\w./-]+\.git|https://[\w.-]+/[\w./-]+(\.git)?)$`)

// rowQueryer lets one renderer read either through the pool or inside an open transaction, so a response can show
// a row exactly as it was written rather than as a concurrent worker later changed it.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inProgressStates is deployments.status while a resource is busy; it is what makes Resource.status 'deploying'.
const inProgressStates = `'queued','preparing','building','starting','verifying','switching','observing'`

// resourceCols is every field of the API's Resource object, derived in one SELECT. Nothing cached here can
// disagree with the engine: status comes from the deployment rows plus desired_state, active_digest from the
// active deployment, primary_domain from the domain table.
const resourceCols = `r.id, r.environment_id, e.project_id, e.name, r.type, r.name, r.slug, r.config_json,
 r.desired_state, r.active_deployment_id,
 (SELECT d.image_digest FROM deployments d WHERE d.id=r.active_deployment_id),
 (SELECT d.hostname FROM domains d WHERE d.resource_id=r.id ORDER BY d.created_at, d.id LIMIT 1),
 CASE
   WHEN EXISTS (SELECT 1 FROM deployments x WHERE x.resource_id=r.id AND x.status IN (` + inProgressStates + `)) THEN 'deploying'
   WHEN r.desired_state='stopped' THEN 'stopped'
   WHEN r.active_deployment_id IS NULL OR r.active_deployment_id='' THEN
     CASE WHEN EXISTS (SELECT 1 FROM deployments y WHERE y.resource_id=r.id AND y.status IN ('failed','interrupted')) THEN 'failed' ELSE 'stopped' END
   ELSE 'running'
 END,
 r.started_at, r.created_at`

const resourceFrom = ` FROM resources r JOIN environments e ON e.id=r.environment_id`

type resourceRow struct {
	ID, EnvID, ProjectID, EnvName, Type, Name, Slug, CfgJSON, Desired, Status string
	Active, Digest, Primary, Started                                          sql.NullString
	Created                                                                   string
}

func scanResource(row *sql.Row) (resourceRow, error) {
	var u resourceRow
	err := row.Scan(&u.ID, &u.EnvID, &u.ProjectID, &u.EnvName, &u.Type, &u.Name, &u.Slug, &u.CfgJSON,
		&u.Desired, &u.Active, &u.Digest, &u.Primary, &u.Status, &u.Started, &u.Created)
	return u, err
}

func scanResourceRows(rs *sql.Rows) (resourceRow, error) {
	var u resourceRow
	err := rs.Scan(&u.ID, &u.EnvID, &u.ProjectID, &u.EnvName, &u.Type, &u.Name, &u.Slug, &u.CfgJSON,
		&u.Desired, &u.Active, &u.Digest, &u.Primary, &u.Status, &u.Started, &u.Created)
	return u, err
}

func (s *Server) resourceByID(ctx context.Context, id string) (resourceRow, error) {
	return scanResource(s.DB.QueryRowContext(ctx, `SELECT `+resourceCols+resourceFrom+` WHERE r.id=?`, id))
}

// response renders the row. config is re-marshalled from the parsed form so a client always sees every key with
// its default filled in; a row the engine cannot parse is returned raw rather than replaced by defaults, because
// inventing a config for someone's running container is worse than showing what is stored.
func (u resourceRow) response() map[string]any {
	nul := func(s sql.NullString) any {
		if s.Valid {
			return s.String
		}
		return nil
	}
	var cfg any
	if parsed, err := deploy.ParseConfig(u.CfgJSON); err != nil {
		cfg = json.RawMessage(u.CfgJSON)
	} else {
		cfg = parsed
	}
	return map[string]any{
		"id": u.ID, "environment_id": u.EnvID, "project_id": u.ProjectID, "environment_name": u.EnvName,
		"type": u.Type, "name": u.Name, "slug": u.Slug, "config": cfg,
		"status": u.Status, "desired_state": u.Desired,
		"active_deployment_id": nul(u.Active), "active_digest": nul(u.Digest), "primary_domain": nul(u.Primary),
		"started_at": nul(u.Started), "created_at": u.Created,
	}
}

// resourceResponse is response() plus urls (public domains + internal network address) for the UI.
func (s *Server) resourceResponse(ctx context.Context, u resourceRow) map[string]any {
	m := u.response()
	m["urls"] = s.resourceURLs(ctx, u)
	return m
}

// resourceURLs lists how operators reach this resource: public HTTPS hostnames and the in-cluster DNS name.
func (s *Server) resourceURLs(ctx context.Context, u resourceRow) map[string]any {
	port := 0
	if parsed, err := deploy.ParseConfig(u.CfgJSON); err == nil {
		port = parsed.Port
	}
	var container any
	var upPort int
	if u.Active.Valid {
		var up sql.NullString
		_ = s.DB.QueryRowContext(ctx, `SELECT upstream FROM deployments WHERE id=?`, u.Active.String).Scan(&up)
		if up.Valid && strings.TrimSpace(up.String) != "" {
			container = strings.TrimSpace(up.String)
			// Prefer the live upstream port over a stale config.port (auto-detect often listens on 8080 while config still says 3000).
			if i := strings.LastIndex(up.String, ":"); i >= 0 {
				if p, err := strconv.Atoi(up.String[i+1:]); err == nil && p > 0 {
					upPort = p
					port = p
				}
			}
		}
	}
	public := make([]map[string]any, 0)
	rows, err := s.DB.QueryContext(ctx, `SELECT hostname, cert_status, force_https FROM domains WHERE resource_id=? AND kind!='panel' ORDER BY created_at, id`, u.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var host, cert string
			var fh int
			if rows.Scan(&host, &cert, &fh) != nil || host == "" {
				continue
			}
			scheme := "https"
			if fh == 0 {
				scheme = "http"
			}
			public = append(public, map[string]any{
				"hostname": host, "url": scheme + "://" + host, "cert_status": cert,
			})
		}
	}
	var primary any
	if len(public) > 0 {
		primary = public[0]["url"]
	} else if u.Primary.Valid && u.Primary.String != "" {
		primary = "https://" + u.Primary.String
	}
	var internal any
	if port > 0 && u.Slug != "" {
		// Docker network alias is the resource slug (see deploy.specFor Aliases).
		internal = u.Slug + ":" + strconv.Itoa(port)
	}
	// Suggest slug.<parent-of-panel-domain> when nothing is attached yet (Coolify-style one-click public URL).
	var suggested any
	if len(public) == 0 && u.Type != "database" && u.Slug != "" {
		var panelHost string
		_ = s.DB.QueryRowContext(ctx, `SELECT hostname FROM domains WHERE kind='panel' LIMIT 1`).Scan(&panelHost)
		if base := serviceDomainBase(panelHost); base != "" {
			host := u.Slug + "." + base
			suggested = map[string]any{
				"hostname": host,
				"url":      "https://" + host,
				"hint":     "Add this domain to expose the app through Caddy (DNS must point here).",
			}
		}
	}
	_ = upPort
	return map[string]any{
		"primary":   primary,
		"public":    public,
		"internal":  internal,  // slug:port for other containers on the environment network
		"container": container, // live deployment upstream (ip:port)
		"suggested": suggested,
	}
}

// serviceDomainBase turns panel.example.com or example.com into example.com for slug.example.com apps.
func serviceDomainBase(panelHost string) string {
	h := strings.ToLower(strings.TrimSpace(panelHost))
	if h == "" || strings.Contains(h, "localhost") || net.ParseIP(h) != nil {
		return ""
	}
	parts := strings.Split(h, ".")
	if len(parts) < 2 {
		return ""
	}
	// panel.example.com → example.com; app.staging.example.com → staging.example.com (keep ≥2 labels)
	if strings.HasPrefix(parts[0], "panel") || parts[0] == "www" || parts[0] == "codeplo" {
		return strings.Join(parts[1:], ".")
	}
	if len(parts) >= 3 {
		return strings.Join(parts[1:], ".")
	}
	return h
}

func (s *Server) findResource(w http.ResponseWriter, r *http.Request, id string) (resourceRow, bool) {
	u, err := s.resourceByID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "resource not found", nil)
		return u, false
	}
	if err != nil {
		internalErr(w, err)
		return u, false
	}
	return u, true
}

// engine returns the deployment engine, or answers for it. Without it the panel would accept resources nothing
// can deploy, so an unwired daemon says so instead of writing rows.
func (s *Server) engine(w http.ResponseWriter, r *http.Request) (*deploy.Engine, bool) {
	if !s.moduleOn(w, r, "deploy") {
		return nil, false
	}
	if s.Deploy == nil {
		writeErr(w, 500, "internal", "The deployment engine is not available on this daemon", nil)
		return nil, false
	}
	return s.Deploy, true
}

func hostMemoryMB() int { return platform.Host().RAMTotalMB }

func isOwner(r *http.Request) bool {
	p := from(r)
	return p != nil && p.User.Role == "owner"
}

// flagOn reads a feature flag; deploy.compose and deploy.static gate the resource types this build cannot deploy.
func (s *Server) flagOn(ctx context.Context, key string) bool {
	var v int
	if err := s.DB.QueryRowContext(ctx, `SELECT enabled FROM feature_flags WHERE key=?`, key).Scan(&v); err != nil {
		return false
	}
	return v == 1
}

// mergeConfigInto applies a request's config over a base config and returns the JSON to store plus the field
// errors. The base is the resource's own config on a patch and the engine's defaults on a create, so a row always
// holds a complete config and the engine and the API read the same object.
func mergeConfigInto(base string, raw json.RawMessage, owner bool) (string, map[string]string, error) {
	start := base
	if strings.TrimSpace(start) == "" {
		b, err := json.Marshal(deploy.DefaultConfig())
		if err != nil {
			return "", nil, err
		}
		start = string(b)
	}
	var merged map[string]any
	if err := json.Unmarshal([]byte(start), &merged); err != nil {
		return "", nil, err
	}
	if len(raw) > 0 && string(raw) != "null" {
		var patch map[string]json.RawMessage
		if err := json.Unmarshal(raw, &patch); err != nil {
			return "", nil, err
		}
		for k, v := range patch {
			merged[k] = v
		}
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return "", nil, err
	}
	cfg, err := deploy.ParseConfig(string(out))
	if err != nil {
		return "", nil, err
	}
	return string(out), cfg.Validate(hostMemoryMB(), owner), nil
}

// ---- projects and environments ----

// deployRoutes are the implemented P4 workload routes; they override the matching laterPhase stubs.
func (s *Server) deployRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/projects", Perm: "resource:read", Module: "deploy", Handler: s.listProjects},
		{Method: "POST", Path: "/projects", Perm: "project:write", Module: "deploy", Handler: s.createProject},
		{Method: "GET", Path: "/projects/{id}", Perm: "resource:read", Module: "deploy", Handler: s.getProject},
		{Method: "GET", Path: "/projects/{id}/environments", Perm: "resource:read", Module: "deploy", Handler: s.listEnvironments},
		{Method: "POST", Path: "/projects/{id}/environments", Perm: "project:write", Module: "deploy", Handler: s.createEnvironment},
		{Method: "GET", Path: "/resources", Perm: "resource:read", Module: "deploy", Handler: s.listResources},
		{Method: "POST", Path: "/resources", Perm: "resource:write", Module: "deploy", Handler: s.createResource},
		{Method: "GET", Path: "/resources/{id}", Perm: "resource:read", Module: "deploy", Handler: s.getResource},
		{Method: "PATCH", Path: "/resources/{id}", Perm: "resource:write", Module: "deploy", Handler: s.patchResource},
		{Method: "DELETE", Path: "/resources/{id}", Perm: "resource:delete", Module: "deploy", Handler: s.deleteResource},
		{Method: "POST", Path: "/resources/{id}/start", Perm: "resource:control", Module: "deploy", Handler: s.startResource},
		{Method: "POST", Path: "/resources/{id}/stop", Perm: "resource:control", Module: "deploy", Handler: s.stopResource},
		{Method: "POST", Path: "/resources/{id}/restart", Perm: "resource:control", Module: "deploy", Handler: s.restartResource},
		{Method: "GET", Path: "/resources/{id}/policy", Perm: "resource:read", Module: "deploy", Handler: s.resourcePolicy},
	}
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	limit, cursor := page(r)
	args := []any{}
	q := `SELECT id, name, slug, description, created_at FROM projects WHERE 1=1`
	if cursor != "" {
		q += ` AND id>?`
		args = append(args, cursor)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.DB.QueryContext(r.Context(), q, args...)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, name, slug, desc, created string
		if err := rows.Scan(&id, &name, &slug, &desc, &created); err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "name": name, "slug": slug, "description": desc, "created_at": created})
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

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	p := from(r)
	var b struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
	}
	if !decode(w, r, &b) {
		return
	}
	f := map[string]string{}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		f["name"] = "Give the project a name"
	}
	if !slugRe.MatchString(b.Slug) {
		f["slug"] = `Use lowercase letters, digits and "-"`
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	id, envID, created := newID("prj_"), newID("env_"), now()
	var conflict bool
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO projects(id,name,slug,description,created_at) VALUES (?,?,?,?,?)`,
			id, name, b.Slug, b.Description, created); err != nil {
			if isUniqueErr(err) {
				conflict = true
				return audit.Event{}, sql.ErrNoRows
			}
			return audit.Event{}, err
		}
		// A project always owns a production environment: resources attach to an environment, never to a project.
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO environments(id,project_id,name,created_at) VALUES (?,?,?,?)`,
			envID, id, "production", created); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "project.created", "project", id, map[string]any{"slug": b.Slug})
	})
	if conflict {
		writeErr(w, 409, "conflict", `A project with slug "`+b.Slug+`" already exists`, nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "name": name, "slug": b.Slug, "description": b.Description, "created_at": created})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	pid := chi.URLParam(r, "id")
	var pname, slug, desc, created string
	err := s.DB.QueryRowContext(r.Context(), `SELECT name, slug, description, created_at FROM projects WHERE id=?`, pid).
		Scan(&pname, &slug, &desc, &created)
	if errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "project not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	envs, err := s.DB.QueryContext(r.Context(), `SELECT id, project_id, name FROM environments WHERE project_id=? ORDER BY id`, pid)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer envs.Close()
	type envRow struct{ id, projectID, name string }
	var list []envRow
	for envs.Next() {
		var x envRow
		if err := envs.Scan(&x.id, &x.projectID, &x.name); err != nil {
			internalErr(w, err)
			return
		}
		list = append(list, x)
	}
	if err := envs.Err(); err != nil {
		internalErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, x := range list {
		rs, err := s.DB.QueryContext(r.Context(), `SELECT `+resourceCols+resourceFrom+` WHERE r.environment_id=? ORDER BY r.id`, x.id)
		if err != nil {
			internalErr(w, err)
			return
		}
		items := []map[string]any{}
		for rs.Next() {
			u, err := scanResourceRows(rs)
			if err != nil {
				rs.Close()
				internalErr(w, err)
				return
			}
			items = append(items, s.resourceResponse(r.Context(), u))
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			internalErr(w, err)
			return
		}
		out = append(out, map[string]any{
			"environment": map[string]any{"id": x.id, "project_id": x.projectID, "name": x.name},
			"resources":   items,
		})
	}
	writeJSON(w, 200, map[string]any{
		"project":      map[string]any{"id": pid, "name": pname, "slug": slug, "description": desc, "created_at": created},
		"environments": out,
	})
}

func (s *Server) listEnvironments(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	pid := chi.URLParam(r, "id")
	var one int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=?`, pid).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "project not found", nil)
		return
	} else if err != nil {
		internalErr(w, err)
		return
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, project_id, name FROM environments WHERE project_id=? ORDER BY id`, pid)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, pp, name string
		if err := rows.Scan(&id, &pp, &name); err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "project_id": pp, "name": name})
	}
	if err := rows.Err(); err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	p := from(r)
	pid := chi.URLParam(r, "id")
	var b struct{ Name string }
	if !decode(w, r, &b) {
		return
	}
	if !slugRe.MatchString(b.Name) {
		invalid(w, map[string]string{"name": `Use lowercase letters, digits and "-"`})
		return
	}
	var one int
	if err := s.DB.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=?`, pid).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		writeErr(w, 404, "not_found", "project not found", nil)
		return
	} else if err != nil {
		internalErr(w, err)
		return
	}
	id, created := newID("env_"), now()
	var conflict bool
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO environments(id,project_id,name,created_at) VALUES (?,?,?,?)`, id, pid, b.Name, created); err != nil {
			if isUniqueErr(err) {
				conflict = true
				return audit.Event{}, sql.ErrNoRows
			}
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "environment.created", "environment", id, map[string]any{"name": b.Name})
	})
	if conflict {
		writeErr(w, 409, "conflict", `Environment "`+b.Name+`" already exists`, nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": id, "project_id": pid, "name": b.Name})
}
