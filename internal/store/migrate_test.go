package store

import (
	"path/filepath"
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
