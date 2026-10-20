package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/edge"
)

const domainCols = `id, hostname, resource_id, kind, force_https, redirect_www, upstream, cert_status, cert_expires_at, cert_error, issuer, created_at`

// domainRow assembles a DomainRoute object. resource_name is always null in this build: the resources
// table arrives in P4, so the hostname's owning resource name cannot be resolved here yet.
func domainRow(id, hostname string, resourceID sql.NullString, kind string, fh, rw int, upstream, certStatus string, exp, certErr sql.NullString, issuer, createdAt string) map[string]any {
	nul := func(s sql.NullString) any {
		if s.Valid {
			return s.String
		}
		return nil
	}
	return map[string]any{
		"id": id, "resource_id": nul(resourceID), "resource_name": nil, "hostname": hostname,
		"is_panel": kind == "panel", "force_https": fh == 1, "redirect_www": rw == 1,
		"cert_status": certStatus, "cert_expires_at": nul(exp), "cert_error": nul(certErr),
		"issuer": issuer, "upstream": upstream, "created_at": createdAt,
	}
}

func (s *Server) writeDomainPage(w http.ResponseWriter, r *http.Request, resourceID string) {
	limit, cursor := page(r)
	q := `SELECT ` + domainCols + ` FROM domains`
	var args []any
	var conds []string
	if resourceID != "" {
		conds = append(conds, `resource_id=?`)
		args = append(args, resourceID)
	}
	if cursor != "" {
		conds = append(conds, `id<?`)
		args = append(args, cursor)
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, ` AND `)
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
		var id, hostname, kind, upstream, certStatus, issuer, createdAt string
		var rid, exp, certErr sql.NullString
		var fh, rw int
		if err := rows.Scan(&id, &hostname, &rid, &kind, &fh, &rw, &upstream, &certStatus, &exp, &certErr, &issuer, &createdAt); err != nil {
			internalErr(w, err)
			return
		}
		items = append(items, domainRow(id, hostname, rid, kind, fh, rw, upstream, certStatus, exp, certErr, issuer, createdAt))
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

func (s *Server) listDomains(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	s.writeDomainPage(w, r, "")
}

func (s *Server) listResourceDomains(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	s.writeDomainPage(w, r, chi.URLParam(r, "id"))
}

// reconcileAfter is best-effort: a domain change wants the new route live now, but if Caddy is down the
// supervisor loop repairs the drift later, so a failure here must never fail the request.
func (s *Server) reconcileAfter() {
	if s.Edge == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := s.Edge.Reconcile(ctx); err != nil {
			slog.Warn("edge reconcile after domain change deferred to loop", "err", err)
		}
	}()
}

func isUniqueErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func (s *Server) createResourceDomain(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	p := from(r)
	rid := chi.URLParam(r, "id")
	var b struct {
		Hostname    string `json:"hostname"`
		ForceHTTPS  *bool  `json:"force_https"`
		RedirectWWW *bool  `json:"redirect_www"`
	}
	if !decode(w, r, &b) {
		return
	}
	h := strings.ToLower(strings.TrimSpace(b.Hostname))
	if len(h) > 253 || !hostRe.MatchString(h) {
		invalid(w, map[string]string{"hostname": "Enter a valid hostname such as app.example.com"})
		return
	}
	fh, rw := true, false
	if b.ForceHTTPS != nil {
		fh = *b.ForceHTTPS
	}
	if b.RedirectWWW != nil {
		rw = *b.RedirectWWW
	}
	id := newID("dom_")
	created := now()
	var reserved, conflict bool
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var kind string
		switch err := tx.QueryRow(`SELECT kind FROM domains WHERE hostname=?`, h).Scan(&kind); err {
		case nil:
			if kind == "panel" {
				reserved = true
			} else {
				conflict = true
			}
			return audit.Event{}, sql.ErrNoRows
		case sql.ErrNoRows:
		default:
			return audit.Event{}, err
		}
		// upstream stays empty in P2 (P4 supplies it); the reconciler skips resource sites without one.
		if _, err := tx.Exec(`INSERT INTO domains(id,hostname,resource_id,kind,force_https,redirect_www,upstream,cert_status,issuer,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, h, rid, "resource", b2i(fh), b2i(rw), "", "issuing", "", created); err != nil {
			if isUniqueErr(err) {
				conflict = true
				return audit.Event{}, sql.ErrNoRows
			}
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "domain.created", "domain", id, map[string]any{"hostname": h, "resource_id": rid})
	})
	if reserved {
		invalid(w, map[string]string{"hostname": "This hostname is reserved for the panel"})
		return
	}
	if conflict {
		writeErr(w, 409, "conflict", h+" is already routed to another resource", map[string]any{"hostname": h})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("edge.changed", map[string]any{"domain_id": id})
	s.reconcileAfter()
	writeJSON(w, 201, map[string]any{
		"id": id, "resource_id": rid, "resource_name": nil, "hostname": h,
		"is_panel": false, "force_https": fh, "redirect_www": rw,
		"cert_status": "issuing", "cert_expires_at": nil, "cert_error": nil,
		"issuer": "", "upstream": "", "created_at": created,
	})
}

// deleteResourceDomain removes one of a resource's domains. The panel domain is never matched because it
// has a NULL resource_id, so attempting it here yields 404 rather than tearing down panel access.
func (s *Server) deleteResourceDomain(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "edge") {
		return
	}
	p := from(r)
	rid, did := chi.URLParam(r, "id"), chi.URLParam(r, "domainId")
	var notFound bool
	var hostname string
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if err := tx.QueryRow(`SELECT hostname FROM domains WHERE id=? AND resource_id=?`, did, rid).Scan(&hostname); err == sql.ErrNoRows {
			notFound = true
			return audit.Event{}, sql.ErrNoRows
		} else if err != nil {
			return audit.Event{}, err
		}
		if _, err := tx.Exec(`DELETE FROM domains WHERE id=?`, did); err != nil {
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "domain.deleted", "domain", did, map[string]any{"hostname": hostname})
	})
	if notFound {
		writeErr(w, 404, "not_found", "domain not found", nil)
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("edge.changed", map[string]any{"domain_id": did})
	s.reconcileAfter()
	w.WriteHeader(204)
}

// setupPanelDomain records the panel's public hostname during first-run setup. It only writes the single
// panel domain row; the loopback listener and SSH-tunnel break-glass path are untouched, so setup can never
// cut its own access. Nothing switches to HTTPS until the reconciler issues the certificate.
func (s *Server) setupPanelDomain(w http.ResponseWriter, r *http.Request) {
	p, _, ok := s.setupSession(w, r)
	if !ok {
		return
	}
	var b struct{ Hostname string }
	if !decode(w, r, &b) {
		return
	}
	h := strings.ToLower(strings.TrimSpace(b.Hostname))
	if len(h) > 253 || !hostRe.MatchString(h) {
		invalid(w, map[string]string{"hostname": "Enter a valid hostname such as panel.example.com"})
		return
	}
	id := newID("dom_")
	var conflict bool
	err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		var kind string
		if err := tx.QueryRow(`SELECT kind FROM domains WHERE hostname=?`, h).Scan(&kind); err == nil && kind != "panel" {
			conflict = true // the hostname is already in use by a resource domain
			return audit.Event{}, sql.ErrNoRows
		} else if err != nil && err != sql.ErrNoRows {
			return audit.Event{}, err
		}
		if err := edge.SetPanelDomain(r.Context(), tx, id, h); err != nil {
			if isUniqueErr(err) {
				conflict = true
				return audit.Event{}, sql.ErrNoRows
			}
			return audit.Event{}, err
		}
		return s.rec(r.Context(), tx, r, p, "setup.panel_domain", "domain", id, map[string]any{"hostname": h})
	})
	if conflict {
		writeErr(w, 409, "conflict", h+" is already routed to a resource", map[string]any{"hostname": h})
		return
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	s.Bus.Publish("edge.changed", map[string]any{"domain_id": id})
	s.reconcileAfter()
	writeJSON(w, 200, map[string]any{"hostname": h, "cert_status": "issuing", "url": nil,
		"detail": "Route added. Nothing switches until the certificate is issued; the loopback and SSH-tunnel paths keep working."})
}
