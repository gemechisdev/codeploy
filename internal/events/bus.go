// Package events: in-process pub/sub used by SSE fan-out.
package events

import "sync"

type Event struct {
	Name string `json:"name"`
	Data any    `json:"data"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

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
	for ch := range b.subs {
		select {
		case ch <- Event{name, data}:
		default:
		}
	}
}
