package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/codeplo/codeplo/internal/modules/runtime"
)

// GET /resources/{id}/logs: server-sent events, one container log line per message. The data format is
// "<RFC3339 timestamp> <text>", which is what web/src/api/hooks/logs.ts parses. Each real line carries an id
// (its timestamp in unix nanoseconds), so the browser's automatic reconnect sends Last-Event-ID and the stream
// resumes after that line instead of replaying the tail.

func (s *Server) logRoutes() []RouteDef {
	return []RouteDef{{Method: "GET", Path: "/resources/{id}/logs", Perm: "resource:read", Module: "deploy", Handler: s.resourceLogs}}
}

const (
	logQueueCap = 512
	logTailMax  = 500
	logTailDef  = 40
)

// dropQueue is a bounded buffer that never blocks the producer: when full it discards the oldest line and
// counts it, so a slow browser loses history instead of stalling the runtime reader or growing memory.
type dropQueue struct {
	mu      sync.Mutex
	items   []runtime.LogLine
	dropped int
	closed  bool
	wake    chan struct{}
}

func newDropQueue() *dropQueue { return &dropQueue{wake: make(chan struct{}, 1)} }

func (q *dropQueue) push(l runtime.LogLine) {
	q.mu.Lock()
	if len(q.items) >= logQueueCap {
		q.items = q.items[1:]
		q.dropped++
	}
	q.items = append(q.items, l)
	q.mu.Unlock()
	q.signal()
}

func (q *dropQueue) close() { q.mu.Lock(); q.closed = true; q.mu.Unlock(); q.signal() }

func (q *dropQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// drain returns everything queued, the number of lines dropped since the last drain, and whether the producer finished.
func (q *dropQueue) drain() ([]runtime.LogLine, int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items, d := q.items, q.dropped
	q.items, q.dropped = nil, 0
	return items, d, q.closed // after close nothing more is pushed, so what was just taken is the last batch
}

func (s *Server) resourceLogs(w http.ResponseWriter, r *http.Request) {
	if !s.moduleOn(w, r, "deploy") {
		return
	}
	if s.Runtime == nil {
		writeErr(w, 500, "internal", "The container runtime is not available", nil)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "internal", "streaming unsupported", nil)
		return
	}
	c, err := s.Runtime.Resolve(r.Context(), chiParam(r, "id"))
	if err != nil {
		switch {
		case errors.Is(err, runtime.ErrNoContainer):
			writeErr(w, 404, "not_found", "No running container for this resource", nil)
		case errors.Is(err, runtime.ErrUnavailable):
			writeErr(w, 500, "internal", "The container runtime is not available", nil)
		default:
			internalErr(w, err)
		}
		return
	}

	opt := runtime.LogOptions{Follow: true, Tail: logTailDef}
	if v := r.URL.Query().Get("tail"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			invalid(w, map[string]string{"tail": "tail must be a non-negative integer"})
			return
		}
		if n > logTailMax {
			n = logTailMax
		}
		opt.Tail = n
	}
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			invalid(w, map[string]string{"since": "since must be an RFC 3339 timestamp"})
			return
		}
		opt.Since = t
	}
	// Resume: Last-Event-ID wins over the query, and a resumed stream must not replay the tail.
	var resumeAfter time.Time
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		if ns, err := strconv.ParseInt(id, 10, 64); err == nil && ns > 0 {
			resumeAfter = time.Unix(0, ns)
			opt.Since, opt.Tail = resumeAfter, 0
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	fmt.Fprint(w, ": connected\n\n")
	fl.Flush()

	q := newDropQueue()
	done := make(chan error, 1)
	go func() {
		err := s.Runtime.Logs(r.Context(), c.ID, opt, func(l runtime.LogLine) error {
			if !resumeAfter.IsZero() && !l.Time.IsZero() && !l.Time.After(resumeAfter) {
				return nil // Docker's since is inclusive enough to repeat the last line
			}
			if l.Time.IsZero() {
				l.Time = time.Now()
			}
			q.push(l)
			return r.Context().Err() // only a gone client stops the reader; a slow one just loses old lines
		})
		q.close()
		done <- err
	}()

	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-q.wake:
			lines, dropped, finished := q.drain()
			if dropped > 0 {
				fmt.Fprintf(w, "data: %s ... %d lines dropped (the browser could not keep up)\n\n", time.Now().UTC().Format(time.RFC3339Nano), dropped)
			}
			for _, l := range lines {
				fmt.Fprintf(w, "id: %d\ndata: %s %s\n\n", l.Time.UnixNano(), l.Time.UTC().Format(time.RFC3339Nano), l.Line)
			}
			fl.Flush()
			if finished {
				// A clean end means the container stopped; tell the client so it does not reconnect. A runtime error
				// leaves the stream open-ended so the browser's reconnect retries.
				if err := <-done; err == nil {
					fmt.Fprint(w, "event: end\ndata: \n\n")
					fl.Flush()
				}
				return
			}
		}
	}
}
