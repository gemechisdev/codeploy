package previews

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/store"
)

func TestCheckWithoutDomain(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	s := New(Deps{DB: db, BaseDomain: "", Now: time.Now})
	st := s.Check(context.Background())
	if st.DomainReady {
		t.Fatal("empty base domain must not be ready")
	}
	if s.Enabled(context.Background()) {
		t.Fatal("previews off without domain/engine/flag")
	}
}

func TestHostnameGrammar(t *testing.T) {
	// label under base domain
	s := New(Deps{BaseDomain: "preview.example.com", Now: time.Now})
	if s.base != "preview.example.com" {
		t.Fatal(s.base)
	}
}
