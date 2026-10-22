package store

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateAllToV7(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codeplo_t1.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var v int
	if err := db.QueryRow(`SELECT version_id FROM goose_db_version ORDER BY id DESC LIMIT 1`).Scan(&v); err != nil {
		t.Fatalf("version query: %v", err)
	}
	if v != 7 {
		t.Fatalf("expected goose_db_version=7, got %d", v)
	}
	t.Logf("goose_db_version=%d OK", v)
}

func TestMigrateSeeds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codeplo_t3.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// settings keys seeded by 0005, 0006, 0007
	wantSettings := []string{
		"git.clone_timeout_s",
		"git.max_clone_mb",
		"metrics.sample_interval_s",
		"metrics.max_samples",
		"alerts.eval_interval_s",
		"notify.timeout_s",
		"logs.retention_days",
		"logs.max_rows",
		"cron.history_per_job",
		"backup.timeout_s",
		"git.preview_ttl_hours",
		"git.preview_max",
		"git.preview_memory_mb",
	}
	for _, key := range wantSettings {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, key).Scan(&n); err != nil {
			t.Fatalf("settings %q: %v", key, err)
		}
		if n != 1 {
			t.Errorf("settings %q: want 1 row, got %d", key, n)
		}
	}

	// feature flag from 0007
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM feature_flags WHERE key = 'git.preview_deploys'`).Scan(&n); err != nil {
		t.Fatalf("feature_flags git.preview_deploys: %v", err)
	}
	if n != 1 {
		t.Errorf("feature_flags git.preview_deploys: want 1 row, got %d", n)
	}
	var enabled int
	if err := db.QueryRow(`SELECT enabled FROM feature_flags WHERE key = 'git.preview_deploys'`).Scan(&enabled); err != nil {
		t.Fatalf("feature_flags enabled: %v", err)
	}
	if enabled != 0 {
		t.Errorf("git.preview_deploys enabled: want 0, got %d", enabled)
	}

	// four disabled alert presets from 0006
	wantAlerts := []struct {
		id, key string
	}{
		{"alr_disk_low", "disk_low"},
		{"alr_container_down", "container_down"},
		{"alr_deploy_failed", "deploy_failed"},
		{"alr_cert_expiring", "cert_expiring"},
	}
	for _, a := range wantAlerts {
		var gotKey string
		var en int
		err := db.QueryRow(`SELECT key, enabled FROM alert_rules WHERE id = ?`, a.id).Scan(&gotKey, &en)
		if err != nil {
			t.Errorf("alert_rules %s: %v", a.id, err)
			continue
		}
		if gotKey != a.key {
			t.Errorf("alert_rules %s: key=%q want %q", a.id, gotKey, a.key)
		}
		if en != 0 {
			t.Errorf("alert_rules %s: enabled=%d want 0", a.id, en)
		}
	}
}

// TestQueryPlans asserts the indexes the code's hot reads actually use (T4).
// Plans are checked for the presence of the intended index name; SQLite may
// still choose a table scan when the table is empty, so we seed minimal rows.
func TestQueryPlans(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codeplo_t4.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, q)
		}
	}
	mustExec(`INSERT INTO projects(id, name, slug, created_at) VALUES ('prj_t4','t4','t4','1970-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO environments(id, project_id, name, created_at) VALUES ('env_t4','prj_t4','prod','1970-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO resources(id, environment_id, type, name, slug, config_json, created_at) VALUES
		('res_parent','env_t4','app','parent','parent','{}','1970-01-01T00:00:00Z'),
		('res_preview','env_t4','app','preview','preview','{}','1970-01-01T00:00:00Z')`)
	mustExec(`INSERT INTO resource_sources(resource_id, kind, repo_url, branch) VALUES
		('res_parent','public_git','https://github.com/acme/app.git','main'),
		('res_preview','public_git','https://github.com/acme/app.git','feat')`)
	mustExec(`INSERT INTO pr_previews(id, provider, parent_resource_id, preview_resource_id, number, head_ref, head_sha, hostname, created_at, updated_at, expires_at)
		VALUES ('prv_1','github','res_parent','res_preview',1,'feat','abc','feat.preview.example',
		'1970-01-01T00:00:00Z','1970-01-01T00:00:00Z','1970-01-02T00:00:00Z')`)
	mustExec(`INSERT INTO metrics_samples(id, at, scope, ref_id) VALUES
		('01ARZ3NDEKTSV4RRFFQ69G5FAV','1970-01-01T00:00:00Z','host',''),
		('01ARZ3NDEKTSV4RRFFQ69G5FB0','1970-01-01T00:00:01Z','host','')`)

	explain := func(sqlText string, args ...any) string {
		t.Helper()
		rows, err := db.Query("EXPLAIN QUERY PLAN "+sqlText, args...)
		if err != nil {
			t.Fatalf("explain: %v\nSQL: %s", err, sqlText)
		}
		defer rows.Close()
		var parts []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			parts = append(parts, detail)
		}
		return strings.Join(parts, " | ")
	}
	assertPlan := func(sqlText string, args []any, wantAny ...string) {
		t.Helper()
		got := explain(sqlText, args...)
		for _, w := range wantAny {
			if strings.Contains(got, w) {
				t.Logf("OK %v → %s", wantAny, got)
				return
			}
		}
		t.Errorf("plan for %s\n  got:  %s\n  want any of: %v", sqlText, got, wantAny)
	}

	// duePreviews: expires_at<=? ORDER BY expires_at, id → pr_previews_expiry
	assertPlan(
		`SELECT id FROM pr_previews WHERE expires_at<=? ORDER BY expires_at, id LIMIT ?`,
		[]any{"1970-01-03T00:00:00Z", 10},
		"pr_previews_expiry",
	)

	// SeriesSince inner: scope=? AND ref_id=? AND id>? ORDER BY id DESC → metrics_samples_window
	assertPlan(
		`SELECT * FROM metrics_samples WHERE scope=? AND ref_id=? AND id>? ORDER BY id DESC LIMIT ?`,
		[]any{"host", "", "01ARZ3NDEKTSV4RRFFQ69G5FAA", 240},
		"metrics_samples_window",
	)

	// oldestPreviews filter: parent_resource_id=? — UNIQUE(provider,parent_resource_id,number)
	// autoindex is preferred over the secondary pr_previews_parent index.
	assertPlan(
		`SELECT id FROM pr_previews WHERE provider='github' AND parent_resource_id=? ORDER BY expires_at, id LIMIT ?`,
		[]any{"res_parent", 5},
		"pr_previews_parent", "sqlite_autoindex_pr_previews",
	)

	// findSourceByRepo: PK scan of resource_sources + NOT IN over pr_previews.
	// No index on preview_resource_id → SCAN pr_previews (acceptable at expected cardinality).
	assertPlan(
		`SELECT resource_id, repo_url FROM resource_sources
			WHERE repo_url IS NOT NULL AND repo_url<>''
			  AND resource_id NOT IN (SELECT preview_resource_id FROM pr_previews)
			ORDER BY resource_id`,
		nil,
		"resource_sources",
	)
}
