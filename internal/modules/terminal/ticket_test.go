package terminal

import (
	"testing"
	"time"
)

func TestTicketSingleUse(t *testing.T) {
	ts := NewTickets(time.Minute)
	tk, _ := ts.Issue("u1", "host", "", false, "https://p.example.com")
	if _, err := ts.Consume(tk.ID, "u1", "host", "", "https://p.example.com"); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if _, err := ts.Consume(tk.ID, "u1", "host", "", "https://p.example.com"); err != ErrTicket {
		t.Fatalf("second use must fail, got %v", err)
	}
}

func TestTicketExpiry(t *testing.T) {
	ts := NewTickets(30 * time.Second)
	base := time.Now()
	ts.now = func() time.Time { return base }
	tk, _ := ts.Issue("u1", "host", "", false, "")
	ts.now = func() time.Time { return base.Add(31 * time.Second) }
	if _, err := ts.Consume(tk.ID, "u1", "host", "", ""); err != ErrTicket {
		t.Fatalf("expired ticket must fail, got %v", err)
	}
}

func TestTicketBindings(t *testing.T) {
	for name, c := range map[string][4]string{
		"wrong user":     {"u2", "host", "", "o"},
		"wrong target":   {"u1", "container", "", "o"},
		"wrong resource": {"u1", "host", "res_1", "o"},
		"wrong origin":   {"u1", "host", "", "https://evil.example"},
	} {
		ts := NewTickets(time.Minute)
		tk, _ := ts.Issue("u1", "host", "", false, "o")
		if _, err := ts.Consume(tk.ID, c[0], c[1], c[2], c[3]); err != ErrTicket {
			t.Errorf("%s: want ErrTicket, got %v", name, err)
		}
		if _, err := ts.Consume(tk.ID, "u1", "host", "", "o"); err != ErrTicket {
			t.Errorf("%s: a failed binding check must still burn the ticket", name)
		}
	}
}

func TestTicketUnknownAndSweep(t *testing.T) {
	ts := NewTickets(time.Minute)
	if _, err := ts.Consume("tk_nope", "u1", "host", "", ""); err != ErrTicket {
		t.Fatal("unknown ticket must fail")
	}
	base := time.Now()
	ts.now = func() time.Time { return base }
	ts.Issue("u1", "host", "", false, "")
	ts.now = func() time.Time { return base.Add(2 * time.Minute) }
	ts.Issue("u1", "host", "", false, "")
	if len(ts.m) != 1 {
		t.Fatalf("expired tickets must be swept on Issue, have %d", len(ts.m))
	}
}
