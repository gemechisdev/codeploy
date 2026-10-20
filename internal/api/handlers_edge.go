package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/edge"
)

// edgeRoutes are the implemented P2 edge + domains routes; they override the matching laterPhase stubs.
func (s *Server) edgeRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/edge/status", Perm: "edge:read", Module: "edge", Handler: s.edgeStatus},
		{Method: "POST", Path: "/edge/reconcile", Perm: "edge:reconcile", Module: "edge", Handler: s.edgeReconcile},
		{Method: "GET", Path: "/edge/certificates", Perm: "edge:read", Module: "edge", Handler: s.edgeCertificates},
		{Method: "GET", Path: "/domains", Perm: "edge:read", Module: "edge", Handler: s.listDomains},
		{Method: "GET", Path: "/resources/{id}/domains", Perm: "resource:read", Module: "edge", Handler: s.listResourceDomains},
		{Method: "POST", Path: "/resources/{id}/domains", Perm: "domain:write", Module: "edge", Handler: s.createResourceDomain},
		{Method: "DELETE", Path: "/resources/{id}/domains/{domainId}", Perm: "domain:write", Module: "edge", Handler: s.deleteResourceDomain},
	}
}

// edgeRec returns the reconciler the daemon wired, or writes an error. A nil here means the module is
// enabled but the daemon never constructed a reconciler (a misconfiguration, not a user-facing state).
func (s *Server) edgeRec(w http.ResponseWriter) *edge.Reconciler {
	if s.Edge == nil {
		writeErr(w, 500, "internal", "The edge reconciler is not available", nil)
		return nil
	}
	return s.Edge
}

// edgeStatus reports the current proxy state. It is read-only: Reconciler.Status GETs the running
// config to compute drift but never POSTs /load, so it cannot change what Caddy serves.
func (s *Server) edgeStatus(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	ec := s.edgeRec(w)
	if ec == nil {
		return
	}
	st, err := ec.Status(r.Context())
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, st)
}

// edgeReconcile pushes the desired config to Caddy. When Caddy is unreachable the reconciler reports
// drift without loading (ErrCaddyDown); the contract maps that to 409 conflict, not a server error.
func (s *Server) edgeReconcile(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	ec := s.edgeRec(w)
	if ec == nil {
		return
	}
	st, err := ec.Reconcile(r.Context())
	if errors.Is(err, edge.ErrCaddyDown) {
		writeErr(w, 409, "conflict", "Caddy is not reachable, so the config could not be loaded",
			map[string]any{"caddy_running": st.CaddyRunning, "drift": st.Drift})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	p := from(r)
	_ = s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "edge.reconcile", "edge", "caddy", map[string]any{"config_hash": st.ConfigHash, "route_count": st.RouteCount})
	})
	s.Bus.Publish("edge.changed", map[string]any{"config_hash": st.ConfigHash})
	writeJSON(w, 200, st)
}

// edgeCertificates lists per-domain issuance status as recorded by the reconciler.
func (s *Server) edgeCertificates(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	ec := s.edgeRec(w)
	if ec == nil {
		return
	}
	certs, err := ec.Certificates(r.Context())
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": certs})
}
