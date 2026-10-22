package monitor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/store"
)

func TestSeriesSinceEmpty(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	s := &Service{DB: db}
	pts, err := s.SeriesSince(context.Background(), db, ScopeHost, "", time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 0 {
		t.Fatalf("want empty, got %d", len(pts))
	}
}
