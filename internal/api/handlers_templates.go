package api

// The template catalog routes (plan P7, J6): what the panel can deploy, and one-click deployment of it.
//
// Both routes answer from the catalog the operator's data directory contributes to, so a dropped-in file shows up on
// the next page load. A file this panel cannot turn into a running container is never listed; it is logged here and
// printed by `codeplo templates check`, because the contract's Template object has no field for a reason.

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/templates"
)

func (s *Server) templateRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/templates", Perm: "template:deploy", Module: "templates", Handler: s.listTemplates},
		{Method: "POST", Path: "/templates/{key}/deploy", Perm: "template:deploy", Module: "templates", Handler: s.deployTemplate},
	}
}

// templateJSON is the contract's Template: eight fields, and nothing else — additionalProperties is false, so an
// extra key would make the panel's own client reject the response.
func templateJSON(t templates.Template) map[string]any {
	return map[string]any{
		"key": t.Key, "name": t.Name, "category": t.Category, "description": t.Description, "icon": t.Icon,
		"default_port": t.DefaultPort, "recommended_memory_mb": t.RecommendedMemoryMB, "kind": string(t.Kind),
	}
}

func (s *Server) listTemplates(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "templates") {
		return
	}
	if s.Templates == nil {
		writeErr(w, 500, "internal", "The template catalog is not available on this daemon", nil)
		return
	}
	list, issues := s.Templates.List()
	for _, is := range issues {
		slog.Warn("template file is not in the catalog", "file", is.File, "reason", is.Reason)
	}
	items := make([]map[string]any, 0, len(list))
	for _, t := range list {
		items = append(items, templateJSON(t))
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// deployTemplate creates the resource a template describes and queues its first deployment. The template decides
// everything the request does not: the type, the image, the port, the memory, and the variables the container starts
// with — which is why the request carries only a project, an environment and a name.
func (s *Server) deployTemplate(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "templates") {
		return
	}
	e, ok := s.engine(w, r)
	if !ok {
		return
	}
	if s.Templates == nil {
		writeErr(w, 500, "internal", "The template catalog is not available on this daemon", nil)
		return
	}
	key := chi.URLParam(r, "key")
	t, err := s.Templates.Get(key)
	if err != nil {
		var nf templates.NotFound
		if errors.As(err, &nf) {
			writeErr(w, 404, "not_found", "template not found", nil)
			return
		}
		// The file is there and this panel cannot deploy it. Saying why is the whole point of refusing: an operator
		// who can see the file in the data directory has to be told what to change in it.
		writeErr(w, 409, "conflict", "This template cannot be deployed by this panel: "+err.Error(), nil)
		return
	}
	p := from(r)
	var b struct {
		ProjectID   string `json:"project_id"`
		Environment string `json:"environment"`
		Name        string `json:"name"`
	}
	if !decode(w, r, &b) {
		return
	}
	f := map[string]string{}
	var project int
	switch err := s.DB.QueryRowContext(r.Context(), `SELECT 1 FROM projects WHERE id=?`, b.ProjectID).Scan(&project); err {
	case nil:
	case sql.ErrNoRows:
		f["project_id"] = "Choose a project"
	default:
		internalErr(w, err)
		return
	}
	var envID string
	if len(f) == 0 {
		switch err := s.DB.QueryRowContext(r.Context(), `SELECT id FROM environments WHERE project_id=? AND name=?`, b.ProjectID, b.Environment).Scan(&envID); err {
		case nil:
		case sql.ErrNoRows:
			f["environment"] = "Choose an environment of that project"
		default:
			internalErr(w, err)
			return
		}
	}
	if !slugRe.MatchString(b.Name) {
		f["name"] = `Use lowercase letters, digits and "-" (1-40 chars, no leading or trailing "-")`
	}
	if len(f) > 0 {
		invalid(w, f)
		return
	}
	// Every template deploy starts its resource: the catalog exists to have something running, and a stopped
	// container nobody asked to queue is not what a Deploy button reads as.
	if !p.Perms["deploy:create"] {
		writeErr(w, 403, "forbidden", "Missing permission: deploy:create", nil)
		return
	}
	plan, err := t.Plan(templates.PlanInput{Name: b.Name})
	if err != nil {
		writeErr(w, 409, "conflict", "This template cannot be deployed by this panel: "+err.Error(), nil)
		return
	}
	// The template's recommended memory is a suggestion the catalog makes about the operator's own machine, so the
	// host gets the last word on it before anything is written.
	if cfgFields := plan.Cfg.Validate(hostMemoryMB(), isOwner(r)); len(cfgFields) > 0 {
		invalid(w, cfgFields)
		return
	}
	d := &resourceDraft{
		EnvironmentID: envID,
		Type:          plan.Type,
		Name:          b.Name,
		ConfigJSON:    plan.ConfigJSON,
		Cfg:           plan.Cfg,
		Source:        sourceInput{Kind: "registry"},
		DeployNow:     true,
	}
	for _, v := range plan.Env {
		d.Env = append(d.Env, envSeed{Key: v.Key, Value: v.Value, Secret: v.Secret})
	}
	for _, v := range plan.Volumes {
		d.Volumes = append(d.Volumes, volumeSeed{Name: v.Name, MountPath: v.MountPath})
	}
	out, ok := s.insertResource(w, r, e, d)
	if !ok {
		return
	}
	// The catalog entry is recorded after the resource exists, in its own transaction: the row that matters is the
	// one that says where a workload came from, and a lost audit row must not unmake a deployed database.
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "template.deployed", "template", key, map[string]any{"resource_id": out.ID, "name": t.Name})
	}); err != nil {
		slog.Warn("template deploy audit row failed", "err", err, "template", key)
	}
	writeJSON(w, 201, map[string]any{"resource": out.Resource, "deployment": out.Deployment})
}
