package terminal

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/codeplo/codeplo/internal/store"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func session(id, user string) *Session {
	return &Session{ID: id, UserID: user, UserEmail: user + "@example.com", IP: "203.0.113.9",
		Target: "host", TargetName: "codeplo-vps", AsUser: "operator"}
}

func TestConfigDefaultsAndOverrides(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	c, err := LoadConfig(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if c.AllowRoot || c.Record {
		t.Errorf("root and recording must default off, got %+v", c)
	}
	if c.IdleTimeout != 15*time.Minute || c.MaxPerUser != 3 {
		t.Errorf("seeded settings not read: %+v", c)
	}
	if c.TicketTTL > 30*time.Second {
		t.Errorf("ticket TTL %v exceeds the contract 30 s", c.TicketTTL)
	}
	if _, err := db.Exec(`UPDATE settings SET value='2' WHERE key='terminal.max_sessions_per_user'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value='true' WHERE key='terminal.allow_root'`); err != nil {
		t.Fatal(err)
	}
	c, _ = LoadConfig(ctx, db)
	if !c.AllowRoot || c.MaxPerUser != 2 {
		t.Errorf("settings override ignored: %+v", c)
	}
}

func TestOpenEnforcesPerUserLimit(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	st := NewStore(db)
	for i, id := range []string{"trm_a", "trm_b", "trm_c"} {
		if err := st.Open(ctx, session(id, "usr_1"), 3); err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
	}
	if err := st.Open(ctx, session("trm_d", "usr_1"), 3); err != ErrLimit {
		t.Fatalf("fourth concurrent shell: want ErrLimit, got %v", err)
	}
	// Another user is unaffected by the first user's shells.
	if err := st.Open(ctx, session("trm_e", "usr_2"), 3); err != nil {
		t.Fatalf("other user blocked: %v", err)
	}
	if err := st.Close(ctx, "trm_b", "exit"); err != nil {
		t.Fatal(err)
	}
	if err := st.Open(ctx, session("trm_f", "usr_1"), 3); err != nil {
		t.Fatalf("a closed shell must free a slot: %v", err)
	}
}

func TestCloseKeepsFirstReason(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	st := NewStore(db)
	if err := st.Open(ctx, session("trm_g", "usr_3"), 0); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(ctx, "trm_g", "idle timeout"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(ctx, "trm_g", "socket closed"); err != nil {
		t.Fatal(err)
	}
	items, _, err := st.List(ctx, 10, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].EndReason != "idle timeout" || items[0].EndedAt == "" {
		t.Fatalf("want one closed row keeping its first reason, got %+v", items)
	}
}

func TestListPagesByKeyset(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	st := NewStore(db)
	for _, id := range []string{"trm_0001", "trm_0002", "trm_0003", "trm_0004"} {
		if err := st.Open(ctx, session(id, "usr_4"), 0); err != nil {
			t.Fatal(err)
		}
	}
	page1, next, err := st.List(ctx, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if next != "trm_0003" || len(page1) != 2 || page1[0].ID != "trm_0004" {
		t.Fatalf("page 1 wrong: next=%q items=%+v", next, page1)
	}
	page2, next2, err := st.List(ctx, 2, next)
	if err != nil {
		t.Fatal(err)
	}
	if next2 != "" || len(page2) != 2 || page2[0].ID != "trm_0002" || page2[1].ID != "trm_0001" {
		t.Fatalf("page 2 wrong (a row was skipped): next=%q items=%+v", next2, page2)
	}
}

// TestRecover proves a crash cannot permanently lock a user out of the shell: rows left open by the previous
// process are closed at start, and max_sessions_per_user is reachable again.
func TestRecoverClosesDanglingRows(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	st := NewStore(db)
	for _, id := range []string{"trm_x", "trm_y", "trm_z"} {
		if err := st.Open(ctx, session(id, "usr_5"), 3); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 dangling rows recovered, got %d", n)
	}
	if _, _, err := st.List(ctx, 10, ""); err != nil {
		t.Fatal(err)
	}
	if n, _ = st.Recover(ctx); n != 0 {
		t.Fatalf("recovery is not idempotent, second pass closed %d rows", n)
	}
	if err := st.Open(ctx, session("trm_w", "usr_5"), 3); err != nil {
		t.Fatalf("user still blocked after recovery: %v", err)
	}
}

func TestSessionJSONMatchesContract(t *testing.T) {
	s := session("trm_j", "usr_6")
	s.StartedAt = iso(time.Now())
	s.IsRoot = true
	got := s.JSON()
	for _, k := range []string{"id", "user_email", "ip", "target", "target_name", "as_user", "is_root", "started_at", "ended_at", "recorded"} {
		if _, ok := got[k]; !ok {
			t.Errorf("TerminalSession object missing %q", k)
		}
	}
	if len(got) != 10 {
		t.Errorf("object carries %d keys, the contract declares 10: %+v", len(got), got)
	}
	if got["ended_at"] != nil {
		t.Errorf("an open session must report ended_at null, got %v", got["ended_at"])
	}
	if got["is_root"] != true || got["recorded"] != false {
		t.Errorf("booleans wrong: %+v", got)
	}
}
