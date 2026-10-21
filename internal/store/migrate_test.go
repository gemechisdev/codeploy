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
