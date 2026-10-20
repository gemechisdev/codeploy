// Package events: in-process pub/sub used by SSE fan-out.
package events

import (
	"sort"
	"strconv"
	"sync"
)

type Event struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Data any    `json:"data"`
}

// Bus is the in-process event bus feeding the SSE stream. It keeps the last 500 events so a client
// can resume after a disconnect with `?last_event_id`, mirroring the frontend mock's bus.
type Bus struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	seq    int
	ring   []Event
	filled int
}

const historyLen = 500

func New() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() { b.mu.Lock(); delete(b.subs, ch); b.mu.Unlock() }
}

// Publish never blocks: slow subscribers drop events (clients refetch on reconnect).
func (b *Bus) Publish(name string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	ev := Event{ID: strconv.Itoa(b.seq), Name: name, Data: data}
	if len(b.ring) < historyLen {
		b.ring = append(b.ring, ev)
	} else {
		b.ring[b.filled] = ev
		b.filled = (b.filled + 1) % historyLen
	}
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Since returns the buffered events with id greater than the given one, oldest first.
// The ids are the bus's monotonic counters, exactly like the mock's.
func (b *Bus) Since(id string) []Event {
	n, err := strconv.Atoi(id)
	if err != nil {
		return []Event{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Event{}
	for _, e := range b.ring {
		if m, _ := strconv.Atoi(e.ID); m > n {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(out[i].ID)
		c, _ := strconv.Atoi(out[j].ID)
		return a < c
	})
	return out
}
