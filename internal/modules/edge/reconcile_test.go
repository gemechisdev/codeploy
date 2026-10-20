package edge

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSetPanelDomainReplacesSingleRow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE domains (id TEXT PRIMARY KEY, hostname TEXT NOT NULL UNIQUE, resource_id TEXT, kind TEXT NOT NULL DEFAULT 'resource',
		force_https INTEGER NOT NULL DEFAULT 1, redirect_www INTEGER NOT NULL DEFAULT 0, upstream TEXT NOT NULL DEFAULT '', cert_status TEXT NOT NULL DEFAULT 'issuing',
		cert_expires_at TEXT, cert_error TEXT, issuer TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);
		CREATE UNIQUE INDEX one_panel ON domains(kind) WHERE kind='panel'`); err != nil {
		t.Fatal(err)
	}
	for i, h := range []string{"a.example.com", "b.example.com"} {
		tx, _ := db.Begin()
		if err := SetPanelDomain(context.Background(), tx, "d"+string(rune('1'+i)), h); err != nil {
			t.Fatal(err)
		}
		_ = tx.Commit()
	}
	var n int
	var h string
	_ = db.QueryRow(`SELECT COUNT(*), MAX(hostname) FROM domains WHERE kind='panel'`).Scan(&n, &h)
	if n != 1 || h != "b.example.com" {
		t.Fatalf("want one panel row b.example.com, got %d %s", n, h)
	}
}
