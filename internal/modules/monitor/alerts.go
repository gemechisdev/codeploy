package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/modules/notify"
	"github.com/codeplo/codeplo/internal/modules/runtime"
)

// Def is one alert condition as the panel knows it. The catalog is code, not data: a rule row stores which
// condition it is and what number the operator chose, and nothing a client can write turns a stored row into a
// condition the evaluator does not implement.
type Def struct {
	Key              string `json:"key"`
	Title            string `json:"title"`
	Condition        string `json:"condition"`
	DefaultThreshold string `json:"threshold"`
}

// Catalog is in the mock's order, which is the order the UI lists them in and the order the "Unknown rule" error
// names them in.
var Catalog = []Def{
	{Key: "disk_low", Title: "Disk space low", Condition: "disk_free_percent <", DefaultThreshold: "15"},
	{Key: "memory_high", Title: "Memory pressure", Condition: "memory_used_percent >", DefaultThreshold: "90"},
	{Key: "container_down", Title: "Container down", Condition: "container_state !=", DefaultThreshold: "running"},
	{Key: "deploy_failed", Title: "Deployment failed", Condition: "deployment.status =", DefaultThreshold: "failed"},
	{Key: "cert_expiring", Title: "Certificate expiring", Condition: "cert_days_left <", DefaultThreshold: "14"},
	{Key: "backup_failed", Title: "Backup failed", Condition: "backup.status =", DefaultThreshold: "failed"},
}

func catalogByKey(key string) (Def, bool) {
	for _, d := range Catalog {
		if d.Key == key {
			return d, true
		}
	}
	return Def{}, false
}

// CatalogKeys is the comma-separated list the unknown-key error quotes, built from the catalog so it cannot drift
// from what the evaluator actually implements.
func CatalogKeys() string {
	out := make([]string, 0, len(Catalog))
	for _, d := range Catalog {
		out = append(out, d.Key)
	}
	return strings.Join(out, ", ")
}

// numericKeys are the conditions whose threshold is a number. The other three compare against a word
// ('running', 'failed') and ignore their threshold when deciding, so it is only ever displayed.
var numericKeys = map[string]bool{"disk_low": true, "memory_high": true, "cert_expiring": true}

// Rule is an alert rule in its API form: the stored row plus the title and condition the catalog supplies.
//
// created_at is kept out of the response because the contract's AlertRule says additionalProperties: false, and a
// field the schema does not name is a field a strict client rejects.
type Rule struct {
	ID              string    `json:"id"`
	Key             string    `json:"key"`
	Title           string    `json:"title"`
	Condition       string    `json:"condition"`
	Threshold       string    `json:"threshold"`
	CooldownS       int       `json:"cooldown_s"`
	Enabled         bool      `json:"enabled"`
	ChannelIDs      []string  `json:"channel_ids"`
	LastTriggeredAt *string   `json:"last_triggered_at"`
	CreatedAt       time.Time `json:"-"`
}

// FieldErrors, NotFound and Conflict are the same three failure shapes the other modules use, so the handler maps
// them once.
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return "validation failed" }

type NotFound struct{ What string }

func (e NotFound) Error() string { return e.What + " not found" }

type Conflict struct{ Msg string }

func (e Conflict) Error() string { return e.Msg }

// CreateRule stores a new rule. It is enabled on creation because the operator has just named the channels to
// notify, which is the choice a preset cannot make for them.
//
// The mock never rejects a threshold; this does, for the three numeric conditions. A threshold of "lots" for
// disk_low is a rule that can never fire, and a silent never-firing alert is the worst failure an operations
// module has: it looks like a healthy host.
func (s *Service) CreateRule(ctx context.Context, q Querier, key, threshold string, cooldownS int, channelIDs []string) (Rule, error) {
	def, ok := catalogByKey(key)
	if !ok {
		return Rule{}, FieldErrors{"key": "Unknown rule. Available: " + CatalogKeys()}
	}
	var existing string
	err := q.QueryRowContext(ctx, `SELECT id FROM alert_rules WHERE key=?`, key).Scan(&existing)
	if err == nil {
		return Rule{}, Conflict{Msg: fmt.Sprintf("A rule for %q already exists", key)}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Rule{}, err
	}
	if threshold == "" {
		threshold = def.DefaultThreshold
	}
	if err := checkThreshold(def.Key, threshold); err != nil {
		return Rule{}, err
	}
	if cooldownS == 0 {
		cooldownS = 900
	}
	if err := checkCooldown(cooldownS); err != nil {
		return Rule{}, err
	}
	if err := s.checkChannels(ctx, q, channelIDs); err != nil {
		return Rule{}, err
	}
	r := Rule{
		ID: s.ruleID(), Key: key, Title: def.Title, Condition: def.Condition, Threshold: threshold,
		CooldownS: cooldownS, Enabled: true, ChannelIDs: orEmpty(channelIDs), CreatedAt: s.now(),
	}
	_, err = q.ExecContext(ctx, `INSERT INTO alert_rules(id,key,threshold,channel_ids_json,cooldown_s,enabled,created_at) VALUES (?,?,?,?,?,?,?)`,
		r.ID, r.Key, r.Threshold, marshalIDs(r.ChannelIDs), r.CooldownS, 1, s.ts(r.CreatedAt))
	if err != nil {
		return Rule{}, err
	}
	return r, nil
}

// PatchRule applies the fields a request names. Threshold, cooldown and channels are validated the same way
// creation validates them, so a rule cannot be enabled for a condition the evaluator will misread.
func (s *Service) PatchRule(ctx context.Context, q Querier, id string, enabled *bool, threshold *string, cooldownS *int, channelIDs *[]string) (Rule, error) {
	r, err := s.GetRule(ctx, q, id)
	if err != nil {
		return Rule{}, err
	}
	if threshold != nil {
		if err := checkThreshold(r.Key, *threshold); err != nil {
			return Rule{}, err
		}
		r.Threshold = *threshold
	}
	if cooldownS != nil {
		if err := checkCooldown(*cooldownS); err != nil {
			return Rule{}, err
		}
		r.CooldownS = *cooldownS
	}
	if channelIDs != nil {
		if err := s.checkChannels(ctx, q, *channelIDs); err != nil {
			return Rule{}, err
		}
		r.ChannelIDs = orEmpty(*channelIDs)
	}
	if enabled != nil {
		r.Enabled = *enabled
	}
	_, err = q.ExecContext(ctx, `UPDATE alert_rules SET threshold=?,cooldown_s=?,channel_ids_json=?,enabled=? WHERE id=?`,
		r.Threshold, r.CooldownS, marshalIDs(r.ChannelIDs), boolInt(r.Enabled), r.ID)
	if err != nil {
		return Rule{}, err
	}
	// Turning a rule off ends its incidents: otherwise the firing rows would still be there when it is switched on
	// again, and a problem that is still present would stay silent for a whole cooldown.
	if !r.Enabled {
		if _, err := q.ExecContext(ctx, `DELETE FROM alert_firings WHERE rule_id=?`, r.ID); err != nil {
			return Rule{}, err
		}
	}
	return r, nil
}

func (s *Service) GetRule(ctx context.Context, q Querier, id string) (Rule, error) {
	var (
		r   Rule
		yes int
		ids string
		la  sql.NullString
		ca  string
	)
	err := q.QueryRowContext(ctx, `SELECT id,key,threshold,channel_ids_json,cooldown_s,enabled,last_triggered_at,created_at FROM alert_rules WHERE id=?`, id).
		Scan(&r.ID, &r.Key, &r.Threshold, &ids, &r.CooldownS, &yes, &la, &ca)
	if errors.Is(err, sql.ErrNoRows) {
		return Rule{}, NotFound{What: "alert rule"}
	}
	if err != nil {
		return Rule{}, err
	}
	return r.fromRow(ids, yes, strValue(la), ca)
}

// ListRules returns every rule in catalog order, so the panel lists the same six rows in the same order whatever
// order they were created in.
func (s *Service) ListRules(ctx context.Context, q Querier) ([]Rule, error) {
	rows, err := q.QueryContext(ctx, `SELECT id,key,threshold,channel_ids_json,cooldown_s,enabled,last_triggered_at,created_at FROM alert_rules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]Rule{}
	for rows.Next() {
		var (
			r   Rule
			yes int
			ids string
			la  sql.NullString
			ca  string
		)
		if err := rows.Scan(&r.ID, &r.Key, &r.Threshold, &ids, &r.CooldownS, &yes, &la, &ca); err != nil {
			return nil, err
		}
		one, err := r.fromRow(ids, yes, strValue(la), ca)
		if err != nil {
			return nil, err
		}
		byKey[one.Key] = one
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Rule{}
	for _, d := range Catalog {
		if r, ok := byKey[d.Key]; ok {
			out = append(out, r)
		}
	}
	// A row whose key is not in the catalog cannot be evaluated, but hiding it would be worse: the operator would
	// have no way to delete what a restore or an old version left behind. It is listed last, with its own key as its
	// title so it reads as what it is.
	for _, r := range byKey {
		if _, ok := catalogByKey(r.Key); !ok {
			out = append(out, r)
		}
	}
	return out, nil
}

// ActiveRuleCount is what /system/status reports as active_alerts: enabled rules that fired in the last six hours,
// which is the mock's definition and the one the UI's badge is built around.
func (s *Service) ActiveRuleCount(ctx context.Context, q Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM alert_rules WHERE enabled=1 AND last_triggered_at IS NOT NULL AND last_triggered_at>?`,
		s.ts(s.now().Add(-6*time.Hour))).Scan(&n)
	return n, err
}

func (r Rule) fromRow(ids string, yes int, lastTriggered *string, createdAt string) (Rule, error) {
	if err := json.Unmarshal([]byte(ids), &r.ChannelIDs); err != nil {
		r.ChannelIDs = []string{}
	}
	if r.ChannelIDs == nil {
		r.ChannelIDs = []string{}
	}
	r.Enabled = yes != 0
	r.LastTriggeredAt = lastTriggered
	r.CreatedAt, _ = time.Parse("2006-01-02T15:04:05.000Z", createdAt)
	if d, ok := catalogByKey(r.Key); ok {
		r.Title, r.Condition = d.Title, d.Condition
	} else {
		r.Title, r.Condition = r.Key, "unrecognised condition"
	}
	return r, nil
}

func (s *Service) ruleID() string { return crypto.ULID("alr_", s.now().UnixMilli()) }

func checkThreshold(key, threshold string) error {
	if !numericKeys[key] {
		return nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(threshold), 64)
	if err != nil || f < 0 {
		return FieldErrors{"threshold": "Threshold must be a number for this rule"}
	}
	return nil
}

func checkCooldown(s int) error {
	if s < 60 {
		return FieldErrors{"cooldown_s": "Cooldown must be at least 60 s"}
	}
	return nil
}

// checkChannels refuses a rule naming a channel that does not exist. The mock answers "Unknown channel"; a rule
// pointing at a deleted channel is accepted by the API today and simply never notifies, so the check is about the
// typo, and the evaluator handles the deletion.
func (s *Service) checkChannels(ctx context.Context, q Querier, ids []string) error {
	for _, id := range ids {
		var exists int
		if err := q.QueryRowContext(ctx, `SELECT 1 FROM notification_channels WHERE id=?`, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return FieldErrors{"channel_ids": "Unknown channel"}
		} else if err != nil {
			return err
		}
	}
	return nil
}

func marshalIDs(ids []string) string {
	b, err := json.Marshal(orEmpty(ids))
	if err != nil {
		return "[]"
	}
	return string(b)
}

func orEmpty(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func strValue(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// firing is one detected instance of a rule's condition. subject is what the incident is about and therefore the
// dedupe key: the host for a disk alert, a resource id for a container that is down, a deployment id for a failed
// deploy. label and measurement are the two strings that end up in the message.
type firing struct {
	subject     string
	label       string
	measurement string
}

// Evaluate runs one pass over the enabled rules: measure, dedupe against what is already firing, notify what is
// new or past its cooldown, and clear what has recovered.
//
// A detector that cannot measure is a warning and a skipped rule, never a failure of the whole pass: Docker being
// down must not stop the disk alert from firing, because a full disk is how the host got there.
func (s *Service) Evaluate(ctx context.Context) error {
	rules, err := s.ListRules(ctx, s.DB)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		fs, err := s.detect(ctx, r)
		if err != nil {
			slog.Warn("alerts: detection failed", "rule", r.Key, "err", err)
			continue
		}
		if err := s.record(ctx, r, fs); err != nil {
			slog.Warn("alerts: recording failed", "rule", r.Key, "err", err)
		}
	}
	return nil
}

// failureLookback bounds how far back the event-shaped rules (a failed deployment, a failed backup) scan. Without
// it, enabling one of those rules on a panel with a year of history would notify every past failure once.
const failureLookback = 24 * time.Hour

func (s *Service) detect(ctx context.Context, r Rule) ([]firing, error) {
	switch r.Key {
	case "disk_low", "memory_high":
		return s.detectHostUsage(ctx, r)
	case "container_down":
		return s.detectContainerDown(ctx, r)
	case "deploy_failed":
		return s.detectDeployFailures(ctx, r)
	case "cert_expiring":
		return s.detectCerts(ctx, r)
	case "backup_failed":
		return s.detectBackupFailures(ctx, r)
	default:
		return nil, nil
	}
}

// detectHostUsage covers the two rules that read the newest host sample.
//
// The sample has to be recent: an hour-old reading on a rule evaluated every minute would alert on a disk that may
// have been cleaned up since, and a stale alarm is worse than none because the operator learns to ignore it.
func (s *Service) detectHostUsage(ctx context.Context, r Rule) ([]firing, error) {
	m, ok, err := s.Latest(ctx, s.DB, ScopeHost, "")
	if err != nil || !ok {
		return nil, err
	}
	at, err := time.Parse("2006-01-02T15:04:05.000Z", m.At)
	if err != nil {
		return nil, fmt.Errorf("unreadable sample timestamp %q", m.At)
	}
	if s.now().Sub(at) > 3*time.Duration(s.IntervalS(ctx))*time.Second {
		return nil, nil
	}
	threshold, err := strconv.ParseFloat(r.Threshold, 64)
	if err != nil {
		return nil, fmt.Errorf("threshold %q is not a number", r.Threshold)
	}
	switch r.Key {
	case "disk_low":
		if m.DiskTotalGB <= 0 {
			return nil, nil
		}
		free := (m.DiskTotalGB - m.DiskUsedGB) / m.DiskTotalGB * 100
		if free >= threshold {
			return nil, nil
		}
		return []firing{{
			subject: "host", label: "host " + s.host().Hostname,
			measurement: fmt.Sprintf("%.1f%% of %0.1f GB free, threshold %.0f%%", free, m.DiskTotalGB, threshold),
		}}, nil
	case "memory_high":
		if m.MemoryTotalMB <= 0 {
			return nil, nil
		}
		used := float64(m.MemoryUsedMB) / float64(m.MemoryTotalMB) * 100
		if used <= threshold {
			return nil, nil
		}
		return []firing{{
			subject: "host", label: "host " + s.host().Hostname,
			measurement: fmt.Sprintf("%d of %d MB used (%.1f%%), threshold %.0f%%", m.MemoryUsedMB, m.MemoryTotalMB, used, threshold),
		}}, nil
	}
	return nil, nil
}

// detectContainerDown fires per resource whose desired state is running and which has no running app container.
//
// Desired state, not the operator's wish: a resource the operator stopped on purpose is not an incident. The
// release-command container is excluded for the same reason the sampler excludes it — it is not the app.
func (s *Service) detectContainerDown(ctx context.Context, r Rule) ([]firing, error) {
	if s.Runtime == nil {
		return nil, nil
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, name FROM resources WHERE desired_state='running'`)
	if err != nil {
		return nil, err
	}
	type res struct{ id, name string }
	var want []res
	for rows.Next() {
		var x res
		if err := rows.Scan(&x.id, &x.name); err != nil {
			rows.Close()
			return nil, err
		}
		want = append(want, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return nil, nil
	}
	states, err := s.containers(ctx)
	if err != nil {
		return nil, err
	}
	up := map[string]bool{}
	status := map[string]string{}
	for _, st := range states {
		if st.Labels[runtime.LabelRole] == runtime.RoleRelease {
			continue
		}
		id := st.Labels[runtime.LabelResource]
		if id == "" {
			continue
		}
		if st.Running {
			up[id] = true
		}
		status[id] = st.Status
	}
	out := []firing{}
	for _, w := range want {
		if up[w.id] {
			continue
		}
		measured := "no container"
		if s2, ok := status[w.id]; ok {
			measured = "state " + s2
		}
		out = append(out, firing{subject: w.id, label: w.name, measurement: measured + ", expected running"})
	}
	return out, nil
}

func (s *Service) detectDeployFailures(ctx context.Context, r Rule) ([]firing, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT d.id, r.name, d.error FROM deployments d JOIN resources r ON r.id=d.resource_id
		WHERE d.status='failed' AND d.finished_at>=? ORDER BY d.finished_at DESC`, s.ts(s.now().Add(-failureLookback)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []firing{}
	for rows.Next() {
		var (
			id, name string
			msg      sql.NullString
		)
		if err := rows.Scan(&id, &name, &msg); err != nil {
			return nil, err
		}
		out = append(out, firing{subject: id, label: name, measurement: orDefault(msg.String, "the deployment failed")})
	}
	return out, rows.Err()
}

// detectCerts reads the domain rows the edge module maintains. A certificate the panel did not issue has no
// expiry stored here, so it cannot alert — and an unknown expiry is reported as no data rather than as a guess.
func (s *Service) detectCerts(ctx context.Context, r Rule) ([]firing, error) {
	days, err := strconv.ParseFloat(r.Threshold, 64)
	if err != nil {
		return nil, fmt.Errorf("threshold %q is not a number of days", r.Threshold)
	}
	limit := s.ts(s.now().Add(time.Duration(days) * 24 * time.Hour))
	rows, err := s.DB.QueryContext(ctx, `SELECT hostname, cert_expires_at FROM domains WHERE cert_status='issued' AND cert_expires_at IS NOT NULL AND cert_expires_at<=?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []firing{}
	for rows.Next() {
		var (
			hostname string
			exp      sql.NullString
		)
		if err := rows.Scan(&hostname, &exp); err != nil {
			return nil, err
		}
		left := "?"
		if t, err := time.Parse("2006-01-02T15:04:05.000Z", exp.String); err == nil {
			left = fmt.Sprintf("%.0f days left", t.Sub(s.now()).Hours()/24)
		}
		out = append(out, firing{subject: hostname, label: hostname, measurement: "certificate expires " + exp.String + " (" + left + ")"})
	}
	return out, rows.Err()
}

func (s *Service) detectBackupFailures(ctx context.Context, r Rule) ([]firing, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT b.id, r.name, b.error FROM backup_runs b
		JOIN backup_schedules s ON s.id=b.schedule_id JOIN resources r ON r.id=s.resource_id
		WHERE b.status='failed' AND b.started_at>=?`, s.ts(s.now().Add(-failureLookback)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []firing{}
	for rows.Next() {
		var (
			id, name string
			msg      sql.NullString
		)
		if err := rows.Scan(&id, &name, &msg); err != nil {
			return nil, err
		}
		out = append(out, firing{subject: id, label: name, measurement: orDefault(msg.String, "the backup run failed")})
	}
	return out, rows.Err()
}

// record turns detections into notifications under the rule's dedupe and cooldown, and closes incidents that are no
// longer detected.
//
// The cooldown is measured from the last notification, not the last detection, and the first detection of an
// incident always notifies: an operator who has just enabled a rule wants to know now, and everything after that is
// on the rule's own schedule.
func (s *Service) record(ctx context.Context, r Rule, fs []firing) error {
	now := s.now()
	keep := make([]string, 0, len(fs))
	for _, f := range fs {
		keep = append(keep, f.subject)
		last, err := s.lastNotified(ctx, r.ID, f.subject)
		if err != nil {
			return err
		}
		fresh := last.IsZero()
		if !fresh && now.Sub(last) < time.Duration(r.CooldownS)*time.Second {
			// Inside the cooldown: count the occurrence and stay quiet.
			if _, err := s.DB.ExecContext(ctx, `UPDATE alert_firings SET occurrences=occurrences+1 WHERE rule_id=? AND subject=?`, r.ID, f.subject); err != nil {
				return err
			}
			continue
		}
		if err := s.reportFiring(ctx, r, f, now, fresh); err != nil {
			return err
		}
	}
	if len(keep) == 0 {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM alert_firings WHERE rule_id=?`, r.ID)
		return err
	}
	placeholders := strings.Repeat("?,", len(keep))
	_, err := s.DB.ExecContext(ctx, `DELETE FROM alert_firings WHERE rule_id=? AND subject NOT IN (`+
		strings.TrimSuffix(placeholders, ",")+`)`, append([]any{r.ID}, anyStrings(keep)...)...)
	return err
}

// lastNotified is when this incident last told anyone. A zero time means it is new.
func (s *Service) lastNotified(ctx context.Context, ruleID, subject string) (time.Time, error) {
	var v string
	err := s.DB.QueryRowContext(ctx, `SELECT last_notified_at FROM alert_firings WHERE rule_id=? AND subject=?`, ruleID, subject).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z", v)
	if err != nil {
		// An unparseable stamp is treated as expired rather than fatal: the worst case is one extra notification,
		// and the alternative is a rule that never notifies again because of one bad row.
		return time.Time{}, nil
	}
	return t, nil
}

// reportFiring sends the alert and records the firing. Successful delivery starts the full cooldown;
// a failed delivery uses a short retry window (~60s) so a downed channel is retried without per-tick spam.
func (s *Service) reportFiring(ctx context.Context, r Rule, f firing, now time.Time, fresh bool) error {
	ts := s.ts(now)
	// last_triggered_at is what /system/status counts as an active alert, so it moves on detection: a rule with no
	// channel configured still has to show that something is wrong.
	if _, err := s.DB.ExecContext(ctx, `UPDATE alert_rules SET last_triggered_at=? WHERE id=?`, ts, r.ID); err != nil {
		return err
	}

	delivered := false
	if s.Notify != nil && len(r.ChannelIDs) > 0 {
		subject := fmt.Sprintf("codeplo alert: %s — %s", r.Title, f.label)
		body := fmt.Sprintf("Condition %s %s\nMeasured: %s\nRule: %s\nAt: %s", r.Condition, r.Threshold, f.measurement, r.Key, ts)
		rep, err := s.Notify.Deliver(ctx, r.ChannelIDs, notify.Message{Subject: subject, Body: body, Level: "alert", At: now})
		if err != nil {
			slog.Warn("alerts: delivery error", "rule", r.Key, "err", err)
		} else if rep.Sent == 0 {
			slog.Warn("alerts: no channel accepted the notification", "rule", r.Key, "detail", rep.Detail)
		} else {
			delivered = true
		}
	}

	// Cooldown clock: full cooldown after a successful send; a short retry window (60s) after a failure so a
	// downed webhook is retried without spamming every evaluator tick.
	notifyAt := now
	if s.Notify != nil && len(r.ChannelIDs) > 0 && !delivered {
		retry := 60 * time.Second
		if time.Duration(r.CooldownS)*time.Second > retry {
			notifyAt = now.Add(-time.Duration(r.CooldownS)*time.Second + retry)
		}
	}
	notifyTS := s.ts(notifyAt)

	if fresh {
		_, err := s.DB.ExecContext(ctx, `INSERT INTO alert_firings(rule_id,subject,first_fired_at,last_notified_at,occurrences) VALUES (?,?,?,?,1)`,
			r.ID, f.subject, ts, notifyTS)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE alert_firings SET last_notified_at=?,occurrences=occurrences+1 WHERE rule_id=? AND subject=?`,
		notifyTS, r.ID, f.subject)
	return err
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func anyStrings(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
