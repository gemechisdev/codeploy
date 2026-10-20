package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrTicket is the only error Consume returns. Expired, used, unknown and mismatched tickets are
// indistinguishable to the caller on purpose: the WebSocket handshake must not become an oracle.
var ErrTicket = errors.New("invalid or expired ticket")

// Ticket authorises exactly one WebSocket upgrade. It is bound to the user, the target, the resource and the
// Origin that requested it (plan P3: single-use, 30 s, Origin match).
type Ticket struct {
	ID         string
	UserID     string
	Target     string // "host" or "container"
	ResourceID string // empty for the host shell
	AsRoot     bool
	Origin     string
	Expires    time.Time
	used       bool
}

// Tickets is an in-memory single-use store. A daemon restart drops every ticket, which costs a user at most one
// 30 s window and removes any need to persist credentials-shaped values.
type Tickets struct {
	mu  sync.Mutex
	m   map[string]*Ticket
	ttl time.Duration
	now func() time.Time
}

func NewTickets(ttl time.Duration) *Tickets {
	if ttl <= 0 {
		ttl = TicketTTL
	}
	return &Tickets{m: map[string]*Ticket{}, ttl: ttl, now: time.Now}
}

// Issue mints a ticket. origin is the Origin header of the issuing request (may be empty for non-browser
// clients, in which case the upgrade must also present an empty Origin).
func (t *Tickets) Issue(userID, target, resourceID string, asRoot bool, origin string) (Ticket, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return Ticket{}, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for k, v := range t.m { // sweep, so abandoned tickets cannot accumulate
		if now.After(v.Expires) || v.used {
			delete(t.m, k)
		}
	}
	tk := &Ticket{ID: "tk_" + hex.EncodeToString(b), UserID: userID, Target: target, ResourceID: resourceID,
		AsRoot: asRoot, Origin: origin, Expires: now.Add(t.ttl)}
	t.m[tk.ID] = tk
	return *tk, nil
}

// Consume validates and burns a ticket. The ticket is marked used even when a binding check fails, so a stolen
// ticket cannot be probed repeatedly with different bindings.
func (t *Tickets) Consume(id, userID, target, resourceID, origin string) (Ticket, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk, ok := t.m[id]
	if !ok {
		return Ticket{}, ErrTicket
	}
	wasUsed := tk.used
	tk.used = true
	if wasUsed || t.now().After(tk.Expires) || tk.UserID != userID || tk.Target != target ||
		tk.ResourceID != resourceID || tk.Origin != origin {
		return Ticket{}, ErrTicket
	}
	return *tk, nil
}
