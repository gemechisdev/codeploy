package previews

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/git"
)

// Request is one delivery's worth of preview work: which service to preview, and the pull request asking for it.
type Request struct {
	ParentResourceID string
	PR               git.PullRequest
}

// parent is the service a preview is built from, read once per delivery.
type parent struct {
	id, envID, rtype, slug, configJSON string
	kind, repoURL                      string
	buildPath, dockerfile              string
}

// Build creates the preview for a pull request, or refreshes the one that already exists, and queues its
// deployment. It returns a refusal reason when the preview cannot be made (which the caller records against the
// delivery and answers with 200), and an error only when the database said no to work it had accepted.
//
// The two are kept strictly apart: every refusal is reached before this function writes anything, so a caller that
// answers 200-and-recorded is never committing a half-built resource that no row describes and no sweep can see.
// What is left to the unique indexes — a slug or hostname taken between the check and the insert — comes back as an
// error, which unwinds the caller's transaction along with the eviction it had already done.
//
// The work runs in the caller's transaction because the caller is the webhook receiver, and the rule there is that
// a delivery id is claimed in the same transaction as the thing it triggers: a preview that exists without its
// delivery row would be built again by GitHub's next retry, and a claimed delivery that triggered nothing is the
// silence this endpoint is not allowed to produce.
//
// A refresh does not create a second resource. The pull request number is the key, so pushing to a branch that
// already has a preview replaces what is running instead of doubling the host's memory, and the link a reviewer was
// given keeps working.
func (s *Service) Build(ctx context.Context, tx *sql.Tx, req Request) (Preview, string, error) {
	if reason := s.BaseDomainProblem(); reason != "" {
		return Preview{}, reason, nil
	}
	if !s.flagOn(ctx, flagPreviews) {
		return Preview{}, "Preview environments are disabled (feature flag " + flagPreviews + ")", nil
	}
	p, ok, err := readParent(ctx, tx, req.ParentResourceID)
	if err != nil {
		return Preview{}, "", err
	}
	if !ok {
		return Preview{}, "The service this pull request would change no longer exists", nil
	}
	if reason := previewable(p); reason != "" {
		return Preview{}, reason, nil
	}
	if req.PR.HeadRef == "" || req.PR.HeadSHA == "" {
		return Preview{}, "The delivery names no branch and commit this panel could clone", nil
	}

	slug := previewSlug(p.slug, p.id, req.PR.Number)
	hostname := slug + "." + s.base
	if len(hostname) > 253 || !hostnameLabelRe.MatchString(slug) {
		return Preview{}, "The preview hostname " + strconv.Quote(hostname) + " is not a name this panel can route", nil
	}
	now := s.now().UTC()
	expires := now.Add(s.ttl(ctx))

	cur, found, err := find(ctx, tx, p.id, req.PR.Number)
	if err != nil {
		return Preview{}, "", err
	}
	if found {
		// The preview row says a workload exists, and the resources row is the only proof that it does. An operator
		// who deleted the preview from its own page left this row holding the hostname and the slug the next build
		// needs, and its delete already queued the reap that stopped the containers — so the stale row is dropped and
		// a fresh preview is built below, rather than queueing a deployment for a resource that is not there. The
		// volume directory is deliberately left alone: this delivery is about to make a preview that mounts it.
		live, err := resourceExists(ctx, tx, cur.ResourceID)
		if err != nil {
			return Preview{}, "", err
		}
		if !live {
			if _, err := tx.ExecContext(ctx, `DELETE FROM pr_previews WHERE id=?`, cur.ID); err != nil {
				return Preview{}, "", err
			}
			found = false
		}
	}
	if found {
		// The head moved, so the record of what is being previewed moves with it — and activity is what keeps a
		// preview alive, which is the difference between a branch someone is working on and one they abandoned.
		if _, err := tx.ExecContext(ctx, `UPDATE pr_previews SET head_ref=?, head_sha=?, updated_at=?, expires_at=? WHERE id=?`,
			req.PR.HeadRef, req.PR.HeadSHA, s.ts(now), s.ts(expires), cur.ID); err != nil {
			return Preview{}, "", err
		}
		depID, err := s.eng.Create(ctx, tx, s.queue, deploy.NewDeployment{
			ResourceID: cur.ResourceID, Trigger: "webhook", Mode: "build", Branch: req.PR.HeadRef,
		})
		if err != nil {
			return Preview{}, "", err
		}
		if _, err := audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "previews",
			Action: "preview.updated", TargetType: "resource", TargetID: cur.ResourceID,
			Meta: map[string]any{"preview_id": cur.ID, "number": req.PR.Number, "deployment_id": depID, "head_sha": req.PR.HeadSHA}}); err != nil {
			return Preview{}, "", err
		}
		cur.HeadRef, cur.HeadSHA, cur.ExpiresAt, cur.UpdatedAt = req.PR.HeadRef, req.PR.HeadSHA, s.ts(expires), s.ts(now)
		return cur, "", nil
	}

	// Occupancy is asked about before anything is written, because a refusal has to mean nothing happened: the caller
	// records the reason against the delivery and answers 200, and a transaction that had already inserted a resource
	// row would commit a stopped workload no row describes and no sweep can see. The two unique indexes stay the last
	// word for a race — there the insert's error unwinds the transaction instead of a refusal being returned from it.
	if taken, err := slugTaken(ctx, tx, slug); err != nil {
		return Preview{}, "", err
	} else if taken {
		return Preview{}, "A service named " + strconv.Quote(slug) + " already exists", nil
	}
	if taken, err := hostnameTaken(ctx, tx, hostname); err != nil {
		return Preview{}, "", err
	} else if taken {
		return Preview{}, "The hostname " + strconv.Quote(hostname) + " is already routed to another resource", nil
	}

	// The cap is per service, because what grows without a cap is one busy repository opening a pull request an
	// hour. Room is made by taking down the preview with the least reason to live rather than by refusing the new
	// one: whoever just opened a request is the reason the feature exists, and the oldest preview is the branch
	// nobody has pushed to in longest.
	max := s.limit(ctx, keyMax, defMax)
	n, err := countPreviews(ctx, tx, p.id)
	if err != nil {
		return Preview{}, "", err
	}
	// One room is made for the preview about to be created, so a service at its limit evicts exactly one.
	evict, err := oldestPreviews(ctx, tx, p.id, n+1-max)
	if err != nil {
		return Preview{}, "", err
	}
	for _, old := range evict {
		if err := s.close(ctx, tx, old, "Replaced to keep within the preview limit"); err != nil {
			return Preview{}, "", err
		}
	}

	cfg, reason := s.previewConfig(ctx, p)
	if reason != "" {
		return Preview{}, reason, nil
	}
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return Preview{}, "", err
	}
	// No volume directory is made here. The engine creates a resource's directories before it starts its container
	// (run.ensureVolumeDirs), and this is not a request an operator is waiting on: making them now would leave an
	// ownerless directory on the disk whenever the transaction below rolls back, and a delivery that GitHub retries
	// reaches the same directory either way.
	vols, err := readVolumes(ctx, tx, p.id)
	if err != nil {
		return Preview{}, "", err
	}

	id := s.id("res_")
	if _, err := tx.ExecContext(ctx, `INSERT INTO resources(id,environment_id,type,name,slug,config_json,desired_state,created_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, p.envID, "app", slug, slug, string(cfgJSON), "stopped", s.ts(now)); err != nil {
		return Preview{}, "", err
	}
	// The preview's source is the parent's repository at the pull request's head. The parent's path filters are
	// deliberately not copied: a pull_request delivery carries no list of changed files, so a filter that lets the
	// parent ignore a README edit would make the preview ignore every pull request.
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_sources(resource_id,kind,repo_url,branch,build_path,dockerfile,path_filters_json,deploy_key_id) VALUES (?,?,?,?,?,?,?,?)`,
		id, p.kind, p.repoURL, req.PR.HeadRef, nilStr(p.buildPath), nilStr(p.dockerfile), "[]", nil); err != nil {
		return Preview{}, "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO domains(id,hostname,resource_id,kind,force_https,redirect_www,upstream,cert_status,issuer,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.id("dom_"), hostname, id, "resource", 1, 0, "", "issuing", "", s.ts(now)); err != nil {
		return Preview{}, "", err
	}
	if err := copyEnv(ctx, tx, s, p.id, id, now); err != nil {
		return Preview{}, "", err
	}
	if err := copyCredentials(ctx, tx, s, p.id, id, now); err != nil {
		return Preview{}, "", err
	}
	for _, v := range vols {
		if _, err := tx.ExecContext(ctx, `INSERT INTO volumes(id,resource_id,name,mount_path,kind,created_at) VALUES (?,?,?,?,?,?)`,
			s.id("vol_"), id, v.name, v.mount, "bind", s.ts(now)); err != nil {
			return Preview{}, "", err
		}
	}
	prev := Preview{
		ID: s.id("prv_"), ParentResourceID: p.id, ResourceID: id, Number: req.PR.Number,
		HeadRef: req.PR.HeadRef, HeadSHA: req.PR.HeadSHA, Hostname: hostname, URL: "https://" + hostname,
		CreatedAt: s.ts(now), UpdatedAt: s.ts(now), ExpiresAt: s.ts(expires),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO pr_previews(id,provider,parent_resource_id,preview_resource_id,number,head_ref,head_sha,hostname,created_at,updated_at,expires_at) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		prev.ID, "github", prev.ParentResourceID, prev.ResourceID, prev.Number, prev.HeadRef, prev.HeadSHA, prev.Hostname,
		prev.CreatedAt, prev.UpdatedAt, prev.ExpiresAt); err != nil {
		// Reaching a unique index here means two deliveries for one pull request raced, and the loser's error is the
		// honest answer: the transaction unwinds — its resource row, its domain and its queued deployment with it —
		// and GitHub's retry of the same delivery finds the preview that won and refreshes it.
		return Preview{}, "", err
	}
	depID, err := s.eng.Create(ctx, tx, s.queue, deploy.NewDeployment{
		ResourceID: id, Trigger: "webhook", Mode: "build", Branch: req.PR.HeadRef,
	})
	if err != nil {
		return Preview{}, "", err
	}
	if _, err := audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "previews",
		Action: "preview.created", TargetType: "resource", TargetID: id,
		Meta: map[string]any{"preview_id": prev.ID, "parent_resource_id": p.id, "number": req.PR.Number,
			"head_ref": req.PR.HeadRef, "hostname": hostname, "deployment_id": depID}}); err != nil {
		return Preview{}, "", err
	}
	return prev, "", nil
}

// previewConfig is the parent's configuration reduced to what a disposable copy of it may run.
//
// Three things are taken out and one is capped. The release command goes first: it is the migrations for the
// parent's database, and a preview would run unreviewed code against real data. Published ports go because the
// parent already holds those loopback bindings, so a preview asking for the same port would fail to start, and one
// that picked a free port would be an unrouted second path to the same workload. Image retention drops to one,
// because a preview rebuilds on every push and three generations of an unmerged branch is disk nobody needs. Memory
// is the smaller of the parent's limit and the preview cap, so a service with a generous limit cannot make its own
// previews expensive.
func (s *Service) previewConfig(ctx context.Context, p parent) (deploy.ResourceConfig, string) {
	cfg, err := deploy.ParseConfig(p.configJSON)
	if err != nil {
		return cfg, "The service's configuration cannot be read, so a preview of it cannot be built"
	}
	cfg.ReleaseCommand = nil
	cfg.PublishedPorts = nil
	cfg.RetainImages = 1
	memCap := s.limit(ctx, keyMemoryMB, defMemoryMB)
	if cfg.Limits.MemoryMB > memCap || cfg.Limits.MemoryMB <= 0 {
		cfg.Limits.MemoryMB = memCap
	}
	return cfg, ""
}

// previewable is the parent-side half of the gate: the things about the service a delivery's pull request cannot
// tell you.
func previewable(p parent) string {
	switch p.rtype {
	case "app":
	case "":
		return "The service this pull request would change no longer exists"
	default:
		return "A preview runs the service's own container, and " + strconv.Quote(p.rtype) + " resources are not previewable"
	}
	if p.repoURL == "" {
		return "The service does not build from a git repository, so there is no branch to preview"
	}
	cfg, err := deploy.ParseConfig(p.configJSON)
	if err != nil {
		return "The service's configuration cannot be read, so a preview of it cannot be built"
	}
	if cfg.Image.Mode != "build" {
		return "The service deploys an image rather than a branch, so a pull request cannot change what runs"
	}
	if cfg.Port <= 0 {
		return "The service has no port for a preview to be routed to"
	}
	return ""
}

// previewSlug names a preview inside the API's own slug rule (1-40 characters, lowercase, no edge dashes) so the
// resource looks like one the operator could have made and is findable by the name they would guess.
//
// When the parent's slug is too long to also carry the suffix, the parent part is cut and a short tag of its id is
// inserted: two services whose slugs share a long prefix must not both want the same preview hostname.
func previewSlug(parentSlug, parentID string, number int) string {
	suffix := "-pr-" + strconv.Itoa(number)
	r := []rune(strings.Trim(parentSlug, "-"))
	if len(r)+len(suffix) <= maxSlugLen {
		return string(r) + suffix
	}
	tag := shortTag(parentID)
	keep := maxSlugLen - len(suffix) - len(tag) - 1
	if keep < 1 {
		keep = 1
	}
	if keep > len(r) {
		keep = len(r)
	}
	return strings.Trim(string(r[:keep]), "-") + "-" + tag + suffix
}

const maxSlugLen = 40

// shortTag is six hex characters of the parent's id — enough that two long slugs with the same prefix get
// different previews, short enough to leave the pull request number room.
func shortTag(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])[:6]
}

// nilStr makes an unset source column NULL rather than "", so a preview of a service with no explicit dockerfile
// inherits "not set" instead of a filename that is empty.
func nilStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// copyEnv duplicates the parent's variables as sealed blobs. Nothing here opens a value: resource_env_vars stores
// bytes the master key sealed, and a preview is the same daemon with the same key, so the copy is an insert of the
// same bytes. It is also why the fork refusal is load-bearing — a preview holds the parent's secrets in the only
// form the panel keeps them.
func copyEnv(ctx context.Context, tx *sql.Tx, s *Service, from, to string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT key, value_enc, scope, is_secret FROM resource_env_vars WHERE resource_id=? ORDER BY id`, from)
	if err != nil {
		return err
	}
	defer rows.Close()
	type seed struct {
		key, scope string
		value      []byte
		secret     int
	}
	var out []seed
	for rows.Next() {
		var sd seed
		if err := rows.Scan(&sd.key, &sd.value, &sd.scope, &sd.secret); err != nil {
			return err
		}
		out = append(out, sd)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, sd := range out {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_env_vars(id,resource_id,key,value_enc,scope,is_secret,updated_at) VALUES (?,?,?,?,?,?,?)`,
			s.id("env_"), to, sd.key, sd.value, sd.scope, sd.secret, s.ts(now)); err != nil {
			return err
		}
	}
	return nil
}

// copyCredentials duplicates the deploy key, registry login and git token the parent's builds use, so a preview of a
// private repository can clone it and pull a private base image. The blobs move unread, for the same reason the
// variables do.
func copyCredentials(ctx context.Context, tx *sql.Tx, s *Service, from, to string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT kind, blob_enc FROM resource_credentials WHERE resource_id=? ORDER BY kind`, from)
	if err != nil {
		return err
	}
	defer rows.Close()
	type cred struct {
		kind string
		blob []byte
	}
	var out []cred
	for rows.Next() {
		var c cred
		if err := rows.Scan(&c.kind, &c.blob); err != nil {
			return err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, c := range out {
		if _, err := tx.ExecContext(ctx, `INSERT INTO resource_credentials(resource_id,kind,blob_enc,updated_at) VALUES (?,?,?,?)`,
			to, c.kind, c.blob, s.ts(now)); err != nil {
			return err
		}
	}
	return nil
}
