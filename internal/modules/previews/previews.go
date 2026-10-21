// Package previews builds pull-request preview environments (plan G8).
//
// A preview is an ordinary resource: it is deployed by the engine, routed by the edge, logged and shell-accessible
// through the same paths as any service, and this package never special-cases any of that. What it owns is the
// judgement around it — whether a delivery may start one, which configuration the parent's service contributes,
// how long the result is allowed to live, and how it gets removed. Keeping only that here is what makes a preview
// deletable by an operator who never reads this code, and reusable by a forge this panel has not met yet.
//
// Nothing in this package is reachable from an HTTP route. The contract's Phase 7 surface is the template catalog,
// so previews arrive the way the cron runner and the commit status do: through the webhook receiver that GitHub
// already posts to, the daemon's loops, and the CLI.
package previews

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/modules/deploy"
)

// Deps is the preview service's whole world. There is no master key here on purpose: a preview duplicates its
// parent's sealed variables and credentials by re-inserting the same bytes, so this package never needs to open one.
type Deps struct {
	DB *sql.DB
	// Engine builds and tears the preview down: Create queues its deployment, ResourceTags and EnqueueReap do the
	// removal. Without it there is nothing to do, so a panel with the deploy module off reports previews as off.
	Engine *deploy.Engine
	// Queue is where a teardown's reap job is written, next to the rows it removes.
	Queue *jobs.Queue
	// BaseDomain is CODEPLO_PREVIEW_DOMAIN: the suffix every preview hostname is built under. It is configuration
	// rather than a setting because it is only useful once the DNS for it exists, and an operator who changed it
	// without changing the record would get certificates that cannot be issued rather than a setting that saves
	// them from that.
	BaseDomain string
	Now        func() time.Time
}

// Service decides and acts on previews.
type Service struct {
	db    *sql.DB
	eng   *deploy.Engine
	queue *jobs.Queue
	base  string
	now   func() time.Time
}

// Preview is one pr_previews row.
type Preview struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	ParentResourceID string `json:"parent_resource_id"`
	ResourceID       string `json:"resource_id"`
	Number           int    `json:"number"`
	HeadRef          string `json:"head_ref"`
	HeadSHA          string `json:"head_sha"`
	Hostname         string `json:"hostname"`
	URL              string `json:"url"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	ExpiresAt        string `json:"expires_at"`
}

const (
	flagPreviews = "git.preview_deploys"
	keyTTLHours  = "git.preview_ttl_hours"
	keyMax       = "git.preview_max"
	keyMemoryMB  = "git.preview_memory_mb"
	// The seeded values. A missing row is a fresh or hand-edited database, and a preview system that read a missing
	// cap as zero would build nothing at all.
	defTTLHours = 72
	defMax      = 5
	defMemoryMB = 512
)

// hostnameLabelRe is one DNS label: lowercase, no leading or trailing punctuation, no dots. A preview hostname is
// exactly one label under the base domain because a wildcard record matches one label and nothing deeper — a
// two-level preview name would silently stop resolving the moment someone moved a repository.
var hostnameLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// baseDomainRe is a bare domain with no scheme, port or path: it is concatenated into hostnames, and everything
// after a slash would be a hostname nobody asked for.
var baseDomainRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)

func New(d Deps) *Service {
	base := strings.ToLower(strings.TrimSpace(d.BaseDomain))
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Service{db: d.DB, eng: d.Engine, queue: d.Queue, base: base, now: now}
}

// BaseDomainProblem explains why this panel cannot build previews, or "" when it can. It is separate from Enabled
// because the two reasons an operator gets an answer of "off" need different sentences: a flag they can turn on, and
// a domain they have to point at this host first.
func (s *Service) BaseDomainProblem() string {
	if p := s.engineProblem(); p != "" {
		return p
	}
	return s.domainProblem()
}

// engineProblem and domainProblem are the two halves apart, because `codeplo previews check` reads the database and
// the environment and has no engine of its own — a command that reported "the engine is not available" about every
// panel would answer nothing. The caller that builds or reaps a preview still asks both, through BaseDomainProblem.
func (s *Service) engineProblem() string {
	if s.eng == nil || s.queue == nil {
		return "The deployment engine is not available on this panel"
	}
	return ""
}

func (s *Service) domainProblem() string {
	if s.base == "" {
		return "No preview base domain is configured (set CODEPLO_PREVIEW_DOMAIN)"
	}
	if !baseDomainRe.MatchString(s.base) {
		return "CODEPLO_PREVIEW_DOMAIN is not a bare domain such as previews.example.com"
	}
	return ""
}

// Enabled reports whether a delivery may start a preview: the feature flag is on and the base domain is configured.
// A panel that set CODEPLO_PREVIEW_DOMAIN but never enabled the flag builds nothing, which is the point of having
// both.
func (s *Service) Enabled(ctx context.Context) bool {
	if s.BaseDomainProblem() != "" {
		return false
	}
	return s.flagOn(ctx, flagPreviews)
}

// Status is the independent facts `codeplo previews check` prints. There is deliberately no verdict field: whether a
// delivery ends up with a preview also needs the engine that builds it, which the command reading the database does
// not carry — so a status that answered "off" from a process that could never have built one would be a fact about
// the reader, not about the panel. The daemon, which holds every part, composes the verdict for its boot log.
type Status struct {
	BaseDomain    string `json:"base_domain"`
	DomainReady   bool   `json:"domain_ready"`
	DomainNote    string `json:"domain_note"`
	FlagOn        bool   `json:"flag_on"`
	TTLHours      int    `json:"ttl_hours"`
	MaxPerService int    `json:"max_per_service"`
	MemoryMB      int    `json:"memory_mb"`
	Live          int    `json:"live"`
}

func (s *Service) Check(ctx context.Context) Status {
	st := Status{BaseDomain: s.base, DomainNote: s.domainProblem()}
	st.DomainReady = st.DomainNote == ""
	st.FlagOn = s.flagOn(ctx, flagPreviews)
	st.TTLHours = int(s.ttl(ctx) / time.Hour)
	st.MaxPerService = s.limit(ctx, keyMax, defMax)
	st.MemoryMB = s.limit(ctx, keyMemoryMB, defMemoryMB)
	st.Live = s.countAll(ctx)
	return st
}

// ttl reads the lifetime setting and clamps it the way the API clamps a write: an operator who set it to 0 meant
// something, and the something they meant was not "expire previews the instant they appear".
func (s *Service) ttl(ctx context.Context) time.Duration {
	h := clamp(s.settingInt(ctx, keyTTLHours, defTTLHours), 1, 720)
	return time.Duration(h) * time.Hour
}

// limit reads one of the two caps with the seeded default as the floor, and never returns zero: a cap of zero
// would refuse every preview while looking like a number the operator chose.
func (s *Service) limit(ctx context.Context, key string, def int) int {
	return clamp(s.settingInt(ctx, key, def), 1, 1000000)
}

func (s *Service) countAll(ctx context.Context) int {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pr_previews`).Scan(&n); err != nil {
		return 0
	}
	return n
}

func (s *Service) flagOn(ctx context.Context, key string) bool {
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT enabled FROM feature_flags WHERE key=?`, key).Scan(&v); err != nil {
		return false
	}
	return v != 0
}

func (s *Service) settingInt(ctx context.Context, key string, def int) int {
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&raw); err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return n
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *Service) ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) id(prefix string) string { return crypto.ULID(prefix, s.now().UnixMilli()) }
