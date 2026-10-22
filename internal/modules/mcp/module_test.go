package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/codeplo/codeplo/internal/store"
)

func TestServeDisabled(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	s := New(db)
	err = s.Serve(context.Background(), bytes.NewReader(nil), &bytes.Buffer{})
	if err == nil {
		t.Fatal("expected disabled error")
	}
}

func TestToolDefsPresent(t *testing.T) {
	s := &Server{}
	if len(s.toolDefs()) < 3 {
		t.Fatal("expected tools")
	}
	_ = json.Marshal
}
