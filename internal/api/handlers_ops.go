// Package api: the Operations surface — metric series, notification channels, alert rules and database backups.
// The rules live in the modules; this file translates between an HTTP request and a module call, and owns nothing
// else. The one exception is deleting a channel, which touches alert_rules because that table belongs to the monitor.
package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/databases"
	"github.com/codeplo/codeplo/internal/modules/monitor"
	"github.com/codeplo/codeplo/internal/modules/notify"
)

func (s *Server) opsRoutes() []RouteDef {
	return []RouteDef{
		{Method: "GET", Path: "/system/metrics", Perm: "system:metrics", Module: "monitor", Handler: s.systemMetrics},
		{Method: "GET", Path: "/notifications/channels", Perm: "notify:manage", Module: "notify", Handler: s.listChannels},
		{Method: "POST", Path: "/notifications/channels", Perm: "notify:manage", Module: "notify", Handler: s.createChannel},
		{Method: "PATCH", Path: "/notifications/channels/{id}", Perm: "notify:manage", Module: "notify", Handler: s.patchChannel},
		{Method: "DELETE", Path: "/notifications/channels/{id}", Perm: "notify:manage", Module: "notify", Handler: s.deleteChannel},
		{Method: "POST", Path: "/notifications/channels/{id}/test", Perm: "notify:manage", Module: "notify", Handler: s.testChannel},
		{Method: "GET", Path: "/alerts/rules", Perm: "notify:manage", Module: "notify", Handler: s.listAlertRules},
		{Method: "POST", Path: "/alerts/rules", Perm: "notify:manage", Module: "notify", Handler: s.createAlertRule},
		{Method: "PATCH", Path: "/alerts/rules/{id}", Perm: "notify:manage", Module: "notify", Handler: s.patchAlertRule},
		{Method: "GET", Path: "/backups/schedules", Perm: "backup:manage", Module: "databases", Handler: s.listBackupSchedules},
		{Method: "GET", Path: "/backups/runs", Perm: "backup:manage", Module: "databases", Handler: s.listBackupRuns},
		{Method: "GET", Path: "/resources/{id}/backups", Perm: "backup:manage", Module: "databases", Handler: s.listResourceBackups},
		{Method: "POST", Path: "/resources/{id}/backups", Perm: "backup:manage", Module: "databases", Handler: s.createBackupSchedule},
		{Method: "POST", Path: "/backups/{id}/run", Perm: "backup:manage", Module: "databases", Handler: s.runBackup},
		{Method: "POST", Path: "/backups/{id}/restore", Perm: "backup:restore", Module: "databases", Handler: s.restoreBackup},
	}
}

// ---- service access and error mapping ----

// The three P6 services are wired by the daemon. A nil one answers 500 with the module named, because a route that
// silently returns an empty list would read as "no channels configured" to an operator who has configured some.

func (s *Server) monitorSvc(w http.ResponseWriter, r *http.Request) (*monitor.Service, bool) {
	if !s.moduleOn(w, r, "monitor") {
		return nil, false
	}
	if s.Monitor == nil {
		writeErr(w, 500, "internal", "The metrics module is not available on this daemon", nil)
		return nil, false
	}
	return s.Monitor, true
}

func (s *Server) notifySvc(w http.ResponseWriter, r *http.Request) (*notify.Service, bool) {
	if !s.moduleOn(w, r, "notify") {
		return nil, false
	}
	if s.Notify == nil {
		writeErr(w, 500, "internal", "The notification module is not available on this daemon", nil)
		return nil, false
	}
	return s.Notify, true
}

func (s *Server) databasesSvc(w http.ResponseWriter, r *http.Request) (*databases.Service, bool) {
	if !s.moduleOn(w, r, "databases") {
		return nil, false
	}
	if s.Databases == nil {
		writeErr(w, 500, "internal", "The database backup module is not available on this daemon", nil)
		return nil, false
	}
	return s.Databases, true
}

// Alert rules are exposed on the notifications surface: the contract tags them
// "notifications" and they require notify:manage, so RouteDef.Module is "notify".
// Their rows, catalog and validation belong to the monitor, though — deleting a
// channel rewrites rules through monitor.PatchRule — so both modules have to be
// on. Gating only on monitor let a request through to the rule service after the
// operator had switched notifications off, which is the exact "module flag is
// ignored" failure the guard test walks the whole route table to catch.
func (s *Server) alertRuleSvc(w http.ResponseWriter, r *http.Request) (*monitor.Service, bool) {
	if !s.moduleOn(w, r, "notify") || !s.moduleOn(w, r, "monitor") {
		return nil, false
	}
	if s.Monitor == nil {
		writeErr(w, 500, "internal", "The metrics module is not available on this daemon", nil)
		return nil, false
	}
	return s.Monitor, true
}

// The three modules each declare their own FieldErrors, NotFound and Conflict: a module must not depend on another
// module's error vocabulary to say what went wrong. So the mapping is written once per package rather than shared.

func notifyErr(w http.ResponseWriter, err error) {
	var fe notify.FieldErrors
	if errors.As(err, &fe) {
		invalid(w, fe)
		return
	}
	var nf notify.NotFound
	if errors.As(err, &nf) {
		writeErr(w, 404, "not_found", nf.Error(), nil)
		return
	}
	var cf notify.Conflict
	if errors.As(err, &cf) {
		writeErr(w, 409, "conflict", cf.Msg, nil)
		return
	}
	if errors.Is(err, notify.ErrSealedKey) {
		writeErr(w, 500, "internal", "A stored channel configuration cannot be opened with this daemon's master key", nil)
		return
	}
	internalErr(w, err)
}

func monitorErr(w http.ResponseWriter, err error) {
	var fe monitor.FieldErrors
	if errors.As(err, &fe) {
		invalid(w, fe)
		return
	}
	var nf monitor.NotFound
	if errors.As(err, &nf) {
		writeErr(w, 404, "not_found", nf.Error(), nil)
		return
	}
	var cf monitor.Conflict
	if errors.As(err, &cf) {
		writeErr(w, 409, "conflict", cf.Msg, nil)
		return
	}
	internalErr(w, err)
}

func databasesErr(w http.ResponseWriter, err error) {
	var fe databases.FieldErrors
	if errors.As(err, &fe) {
		invalid(w, fe)
		return
	}
	var nf databases.NotFound
	if errors.As(err, &nf) {
		writeErr(w, 404, "not_found", nf.Error(), nil)
		return
	}
	var cf databases.Conflict
	if errors.As(err, &cf) {
		writeErr(w, 409, "conflict", cf.Msg, nil)
		return
	}
	internalErr(w, err)
}

// ---- metrics ----

// systemMetrics serves one chart. The window decides both how far back to read and how many points to send, so a
// 24-hour chart costs the same bytes as a 6-hour one; an unknown range is a client bug and says so in the field the
// form owns.
func (s *Server) systemMetrics(w http.ResponseWriter, r *http.Request) {
	m, ok := s.monitorSvc(w, r)
	if !ok {
		return
	}
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "1h"
	}
	points, ok := monitor.RangePoints(name)
	if !ok {
		invalid(w, map[string]string{"range": "Use 15m, 1h, 6h or 24h"})
		return
	}
	window, _ := monitor.RangeWindow(name)
	series, err := m.SeriesSince(r.Context(), s.DB, monitor.ScopeHost, "", time.Now().Add(-window), points)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"range": name, "points": series})
}

// ---- notification channels ----

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	n, ok := s.notifySvc(w, r)
	if !ok {
		return
	}
	items, err := n.ListChannels(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// createChannel stores a channel whose configuration is sealed on the way in and never read back: the response is
// the channel's name and a destination hint, and no route on this panel returns the token it was given.
func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	n, ok := s.notifySvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	var b struct {
		Kind   string            `json:"kind"`
		Name   string            `json:"name"`
		Config map[string]string `json:"config"`
	}
	if !decode(w, r, &b) {
		return
	}
	out := notify.Channel{}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		ch, err := n.CreateChannel(r.Context(), tx, b.Kind, b.Name, notify.Config(b.Config))
		if err != nil {
			return audit.Event{}, err
		}
		out = ch
		return s.rec(r.Context(), tx, r, p, "channel.created", "channel", ch.ID, map[string]any{"kind": ch.Kind})
	}); err != nil {
		notifyErr(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) patchChannel(w http.ResponseWriter, r *http.Request) {
	n, ok := s.notifySvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	var b struct {
		Name    *string `json:"name"`
		Enabled *bool   `json:"enabled"`
	}
	if !decode(w, r, &b) {
		return
	}
	id := chi.URLParam(r, "id")
	out := notify.Channel{}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		ch, err := n.PatchChannel(r.Context(), tx, id, b.Name, b.Enabled)
		if err != nil {
			return audit.Event{}, err
		}
		out = ch
		return s.rec(r.Context(), tx, r, p, "channel.updated", "channel", ch.ID, map[string]any{"enabled": ch.Enabled})
	}); err != nil {
		notifyErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// deleteChannel also strips the channel from every alert rule that names it. The rules belong to the monitor, which
// owns their validation, so the edit goes through PatchRule rather than through an UPDATE here; a daemon without the
// monitor has no rules to leave dangling, and Deliver already skips a channel that vanished.
func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	n, ok := s.notifySvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	id := chi.URLParam(r, "id")
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		if err := n.DeleteChannel(r.Context(), tx, id); err != nil {
			return audit.Event{}, err
		}
		if s.Monitor != nil {
			rules, err := s.Monitor.ListRules(r.Context(), tx)
			if err != nil {
				return audit.Event{}, err
			}
			for _, ru := range rules {
				kept := make([]string, 0, len(ru.ChannelIDs))
				for _, c := range ru.ChannelIDs {
					if c != id {
						kept = append(kept, c)
					}
				}
				if len(kept) == len(ru.ChannelIDs) {
					continue
				}
				if _, err := s.Monitor.PatchRule(r.Context(), tx, ru.ID, nil, nil, nil, &kept); err != nil {
					return audit.Event{}, err
				}
			}
		}
		return s.rec(r.Context(), tx, r, p, "channel.deleted", "channel", id, map[string]any{})
	}); err != nil {
		notifyErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// testChannel is the one route whose failure is a normal answer: a wrong bot token is a result the operator reads,
// not an error page, so 200 carries ok:false with the provider's reason.
func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	n, ok := s.notifySvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	id := chi.URLParam(r, "id")
	good, detail, err := n.Test(r.Context(), id)
	if err != nil {
		notifyErr(w, err)
		return
	}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "channel.tested", "channel", id, map[string]any{"ok": good})
	}); err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": good, "detail": detail})
}

// ---- alert rules ----

func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alertRuleSvc(w, r)
	if !ok {
		return
	}
	items, err := m.ListRules(r.Context(), s.DB)
	if err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alertRuleSvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	var b struct {
		Key        string   `json:"key"`
		Threshold  string   `json:"threshold"`
		CooldownS  int      `json:"cooldown_s"`
		ChannelIDs []string `json:"channel_ids"`
	}
	if !decode(w, r, &b) {
		return
	}
	out := monitor.Rule{}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		ru, err := m.CreateRule(r.Context(), tx, b.Key, b.Threshold, b.CooldownS, b.ChannelIDs)
		if err != nil {
			return audit.Event{}, err
		}
		out = ru
		return s.rec(r.Context(), tx, r, p, "alert_rule.created", "alert_rule", ru.ID, map[string]any{"key": ru.Key})
	}); err != nil {
		monitorErr(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) patchAlertRule(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alertRuleSvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	var b struct {
		Enabled    *bool     `json:"enabled"`
		Threshold  *string   `json:"threshold"`
		CooldownS  *int      `json:"cooldown_s"`
		ChannelIDs *[]string `json:"channel_ids"`
	}
	if !decode(w, r, &b) {
		return
	}
	id := chi.URLParam(r, "id")
	out := monitor.Rule{}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		ru, err := m.PatchRule(r.Context(), tx, id, b.Enabled, b.Threshold, b.CooldownS, b.ChannelIDs)
		if err != nil {
			return audit.Event{}, err
		}
		out = ru
		return s.rec(r.Context(), tx, r, p, "alert_rule.updated", "alert_rule", ru.ID, map[string]any{"enabled": ru.Enabled})
	}); err != nil {
		monitorErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// ---- backup schedules ----

func (s *Server) listBackupSchedules(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databasesSvc(w, r)
	if !ok {
		return
	}
	items, err := d.ListSchedules(r.Context(), s.DB, "")
	if err != nil {
		internalErr(w, err)
		return
	}
	if items == nil {
		items = []databases.Schedule{}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) listResourceBackups(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databasesSvc(w, r)
	if !ok {
		return
	}
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	items, err := d.ListSchedules(r.Context(), s.DB, cur.ID)
	if err != nil {
		internalErr(w, err)
		return
	}
	if items == nil {
		items = []databases.Schedule{}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// createBackupSchedule validates the resource before it stores anything: a schedule for a database this panel
// cannot dump is a mistake about the resource, and the operator should hear it now rather than at 03:00.
func (s *Server) createBackupSchedule(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databasesSvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	cur, ok := s.findResource(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var b struct {
		Cron          string `json:"cron"`
		Destination   string `json:"destination"`
		RetentionDays int    `json:"retention_days"`
	}
	if !decode(w, r, &b) {
		return
	}
	out := databases.Schedule{}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		sc, err := d.CreateSchedule(r.Context(), tx, databases.CreateScheduleInput{
			ResourceID: cur.ID, Cron: b.Cron, Destination: b.Destination, RetentionDays: b.RetentionDays,
		})
		if err != nil {
			return audit.Event{}, err
		}
		out = sc
		return s.rec(r.Context(), tx, r, p, "backup.schedule_created", "resource", cur.ID,
			map[string]any{"cron": sc.Cron, "destination": sc.Destination})
	}); err != nil {
		databasesErr(w, err)
		return
	}
	writeJSON(w, 201, out)
}

// ---- backup runs and restore ----

func (s *Server) listBackupRuns(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databasesSvc(w, r)
	if !ok {
		return
	}
	limit, cursor := page(r)
	items, next, err := d.ListRuns(r.Context(), s.DB, limit, cursor)
	if err != nil {
		internalErr(w, err)
		return
	}
	if items == nil {
		items = []databases.Run{}
	}
	var n any
	if next != "" {
		n = next
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": n})
}

// runBackup opens the attempt and queues the work in the same transaction, then answers 202: a dump of a real
// database outlives any reasonable HTTP timeout, and the operator watching the run list sees it finish there.
func (s *Server) runBackup(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databasesSvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	id := chi.URLParam(r, "id")
	out := databases.Run{}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		run, err := d.StartRun(r.Context(), tx, id, databases.TriggerManual)
		if err != nil {
			return audit.Event{}, err
		}
		if _, err := s.Jobs.Enqueue(r.Context(), tx, databases.JobRun, run.ID, time.Now()); err != nil {
			return audit.Event{}, err
		}
		out = run
		return s.rec(r.Context(), tx, r, p, "backup.run", "backup", run.ID, map[string]any{"schedule": run.ScheduleID})
	}); err != nil {
		databasesErr(w, err)
		return
	}
	writeJSON(w, 202, out)
}

// restoreBackup is the only P6 route behind step-up, and it restores nothing until the module has taken and
// verified a snapshot of the database as it stands now.
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	d, ok := s.databasesSvc(w, r)
	if !ok {
		return
	}
	p := from(r)
	id := chi.URLParam(r, "id")
	rn, err := d.GetRun(r.Context(), s.DB, id)
	if err != nil {
		databasesErr(w, err)
		return
	}
	if !requireStepUp(w, p) {
		return
	}
	res, err := d.Restore(r.Context(), rn.ID)
	if err != nil {
		databasesErr(w, err)
		return
	}
	if err := s.tx(r, func(tx *sql.Tx) (audit.Event, error) {
		return s.rec(r.Context(), tx, r, p, "backup.restored", "backup", rn.ID,
			map[string]any{"pre_restore_snapshot": res.PreRestoreSnapshot})
	}); err != nil {
		internalErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}
