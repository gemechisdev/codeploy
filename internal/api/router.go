// Package api: chi router, middleware, error envelope and the single route table that the authz matrix test reads.
package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/codeplo/codeplo/internal/authn"
	"github.com/codeplo/codeplo/internal/authz"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/events"
	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/modules"
	"github.com/codeplo/codeplo/internal/modules/edge"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/modules/terminal"
	"github.com/codeplo/codeplo/internal/webui"
)

const Prefix = "/api/v1"

// RouteDef declares exactly one required permission per route (authz.Public, authz.Session or a registry permission).
type RouteDef struct {
	Method, Path, Perm, Module string
	Handler                    http.HandlerFunc
	NoCSRF                     bool // setup-session routes (SameSite=Strict + setup cookie instead)
}

type Server struct {
	DB   *sql.DB
	Key  []byte
	Bus  *events.Bus
	Reg  *modules.Registry
	Jobs *jobs.Queue
	Edge *edge.Reconciler // wired by the daemon when the edge module is enabled; nil otherwise
	// Runtime and Terminals are the P3 ports; both stay nil when the daemon cannot reach the wiring, and the
	// handlers answer 500 rather than pretending a shell is available.
	Runtime    runtime.Runtime
	Terminals  *terminal.Store
	Tickets    *terminal.Tickets // single-use WebSocket tickets, in memory
	HostShells terminal.Spawner  // privileged PTY helper client; nil until wired
	Started    time.Time
	Version    string
	Commit     string
	BuiltAt    string
	lim        *limiter
	replay     *authn.ReplayGuard
}

func New(db *sql.DB, key []byte, version, commit, built string) *Server {
	return &Server{DB: db, Key: key, Bus: events.New(), Reg: modules.NewRegistry(db), Jobs: jobs.New(db),
		Started: time.Now(), Version: version, Commit: commit, BuiltAt: built, lim: newLimiter(), replay: authn.NewReplayGuard()}
}

// Routes returns every route in the API (implemented core routes followed by later-phase stubs).
func (s *Server) Routes() []RouteDef {
	defs := append(append(s.coreRoutes(), s.hardeningRoutes()...), s.edgeRoutes()...)
	defs = append(append(defs, s.terminalRoutes()...), s.logRoutes()...)
	implemented := map[string]bool{}
	for _, d := range defs {
		implemented[d.Method+" "+d.Path] = true
	}
	for _, d := range laterPhase {
		if implemented[d.Method+" "+d.Path] {
			continue // implemented routes override the generated stub
		}
		d.Handler = s.stub(d.Module)
		defs = append(defs, d)
	}
	return defs
}

type listenerKey struct{}

// Handler builds the full http.Handler. kind is "loopback" or "unix" (only "unix" may carry trusted X-Forwarded-*).
func (s *Server) Handler(kind string) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer, securityHeaders, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), listenerKey{}, kind)))
		})
	})
	r.Route(Prefix, func(api chi.Router) {
		for _, d := range s.Routes() {
			api.Method(d.Method, d.Path, s.guard(d))
		}
		api.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeErr(w, 404, "not_found", "no such endpoint", nil) })
	})
	r.NotFound(webui.Handler().ServeHTTP)
	return r
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil && v != http.ErrAbortHandler {
				writeErr(w, 500, "internal", "internal error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, Prefix) {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// ---- error envelope & helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	b := map[string]any{"code": code, "message": msg}
	if details != nil {
		b["details"] = details
	}
	writeJSON(w, status, b)
}

func invalid(w http.ResponseWriter, fields map[string]string) {
	writeErr(w, 422, "validation_failed", "Validation failed", map[string]any{"fields": fields})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		invalid(w, map[string]string{"body": "Invalid JSON body"})
		return false
	}
	return true
}

func internalErr(w http.ResponseWriter, err error) {
	_ = err // never leak internals; the daemon logs via slog at call sites that care
	writeErr(w, 500, "internal", "internal error", nil)
}

func page(r *http.Request) (limit int, cursor string) {
	limit = 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > 200 {
		limit = 200
	}
	return limit, r.URL.Query().Get("cursor")
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
func now() string            { return iso(time.Now()) }
func newID(p string) string  { return crypto.ULID(p, time.Now().UnixMilli()) }

func clientIP(r *http.Request) string {
	if k, _ := r.Context().Value(listenerKey{}).(string); k == "unix" {
		if x := r.Header.Get("X-Forwarded-For"); x != "" {
			parts := strings.Split(x, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// ---- guard: rate limit -> authn -> owner-TOTP gate -> CSRF -> permission ----

type ctxKey struct{}

func from(r *http.Request) *Principal { p, _ := r.Context().Value(ctxKey{}).(*Principal); return p }

func (s *Server) guard(d RouteDef) http.Handler {
	if !authz.Valid(d.Perm) {
		panic("route " + d.Method + " " + d.Path + " declares unknown permission " + d.Perm)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, Prefix)
		if strings.HasPrefix(p, "/auth/") || strings.HasPrefix(p, "/setup/") || strings.HasPrefix(p, "/hooks/") || strings.HasPrefix(p, "/webhooks/") {
			if ok, wait := s.lim.allow("ip:"+clientIP(r), 30, 1); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(wait))
				writeErr(w, 429, "rate_limited", "Too many requests. Try again shortly.", nil)
				return
			}
		}
		if d.Perm == authz.Public {
			s.attach(r) // optional principal (setup/login handlers inspect it)
			if pr := from(r); pr != nil {
				r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, pr))
			}
			d.Handler(w, r)
			return
		}
		pr, err := s.authenticate(r)
		if err != nil || pr == nil || pr.Pending != "" {
			writeErr(w, 401, "unauthenticated", "Not signed in", nil)
			return
		}
		if pr.User.Role == "owner" && !pr.User.TOTPEnabled && p != "/auth/me" && p != "/auth/logout" {
			writeErr(w, 403, "forbidden", "The owner must enroll TOTP before any other action", nil)
			return
		}
		if !pr.Token && r.Method != "GET" && r.Method != "HEAD" && !crypto.Equal(r.Header.Get("X-CSRF-Token"), pr.CSRF) {
			writeErr(w, 403, "forbidden", "Missing or invalid CSRF token", nil)
			return
		}
		if d.Perm != authz.Session && !pr.Perms[d.Perm] {
			writeErr(w, 403, "forbidden", "Missing permission: "+d.Perm, nil)
			return
		}
		d.Handler(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, pr)))
	})
}

func (s *Server) attach(r *http.Request) {
	if pr, _ := s.authenticate(r); pr != nil {
		*r = *r.WithContext(context.WithValue(r.Context(), ctxKey{}, pr))
	}
}

// stub serves a route owned by a later phase: 404 module_disabled when the module is off, else 501.
func (s *Server) stub(module string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Reg.Enabled(r.Context(), module) {
			writeErr(w, 404, "module_disabled", "The "+module+" module is disabled", map[string]any{"module": module})
			return
		}
		writeErr(w, 501, "module_disabled", "The "+module+" module is not implemented in this build", map[string]any{"module": module, "implemented": false})
	}
}

// ---- token bucket ----

type bucket struct {
	tokens float64
	at     time.Time
}
type limiter struct {
	mu sync.Mutex
	m  map[string]*bucket
}

func newLimiter() *limiter { return &limiter{m: map[string]*bucket{}} }

// allow takes one token from a bucket with the given burst and refill rate (tokens/second).
func (l *limiter) allow(key string, burst, perSec float64) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := time.Now()
	b := l.m[key]
	if b == nil {
		if len(l.m) > 10000 {
			l.m = map[string]*bucket{}
		}
		b = &bucket{tokens: burst, at: t}
		l.m[key] = b
	}
	b.tokens += t.Sub(b.at).Seconds() * perSec
	if b.tokens > burst {
		b.tokens = burst
	}
	b.at = t
	if b.tokens < 1 {
		return false, int((1-b.tokens)/perSec) + 1
	}
	b.tokens--
	return true, 0
}
