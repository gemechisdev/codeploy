package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/policy"
)

// This file is the engine's surface for the REST API: the questions the panel asks about a resource without
// running a deployment (what would the isolation policy say, what does this resource cost to keep running), and
// the durable side effects of the actions that do touch containers (stop, delete, cleanup).
//
// Container work that the API triggers is enqueued as a job rather than done inline. A request must not hang on a
// Docker daemon that is slow or down, and a half-done stop must be retried after a restart; the queue gives both.

// PolicyResult is GET /resources/{id}/policy (OpenAPI PolicyResult).
type PolicyResult struct {
	ResourceID string             `json:"resource_id"`
	OK         bool               `json:"ok"`
	Controls   []policy.Control   `json:"controls"`
	Violations []string           `json:"violations"`
	Warnings   []policy.Violation `json:"-"`
}

// Policy describes the isolation verdict the engine would reach for this resource's current config. It runs the
// same policy.Enforce the deployment does, on the same container spec, so the table the panel shows cannot disagree
// with what the engine enforces later. No env values are needed: the policy judges mounts, caps, ports and limits.
func (e *Engine) Policy(ctx context.Context, resourceID string) (PolicyResult, error) {
	res, err := loadResource(ctx, e.db, resourceID)
	if err != nil {
		return PolicyResult{}, err
	}
	vols, err := loadVolumes(ctx, e.db, resourceID)
	if err != nil {
		return PolicyResult{}, err
	}
	spec := specForConfig(res, vols, e.cfg.VolumesRoot)
	opts := optionsFor(res, e.cfg)
	out := policy.Enforce(spec, opts)
	msgs := out.Messages()
	if msgs == nil {
		msgs = []string{} // the contract types violations as an array; null would break every client's .length
	}
	return PolicyResult{
		ResourceID: resourceID,
		OK:         len(out.Violations) == 0,
		Controls:   policy.Describe(spec, out, opts, res.EnvName),
		Violations: msgs,
		Warnings:   out.Warnings,
	}, nil
}

// CheckPolicy is the deploy-time gate: the same verdict, reduced to "may this deploy start at all".
func (e *Engine) CheckPolicy(ctx context.Context, resourceID string) ([]string, error) {
	p, err := e.Policy(ctx, resourceID)
	return p.Violations, err
}

// specForConfig builds the requested container for a resource outside a deployment run. The names are the ones a
// real run would use, with a placeholder deployment, so what the policy sees is what would be created.
func specForConfig(res Resource, vols []Volume, volumesRoot string) policy.Container {
	cfg := res.Cfg
	tag := "codeplo/" + res.Slug + ":"
	if res.Active != "" {
		tag += res.Active
	} else {
		tag += "pending"
	}
	c := policy.Container{
		Service: "app", Name: "cpl-" + res.Slug + "-policy", Image: tag, Restart: cfg.Restart,
		Aliases: []string{res.Slug}, CapAdd: cfg.Isolation.CapAdd, Network: "cpl-env-" + res.EnvID,
		MemoryBytes: int64(cfg.Limits.MemoryMB) << 20, NanoCPUs: int64(cfg.Limits.CPUs * 1e9), PidsLimit: int64(cfg.Limits.PidsLimit),
	}
	if cfg.Isolation.User != nil {
		c.User = *cfg.Isolation.User
	}
	for _, v := range vols {
		c.Mounts = append(c.Mounts, policy.Mount{Type: "bind", Source: path.Join(volumesRoot, res.Slug, v.Name), Target: v.MountPath})
	}
	for _, p := range cfg.PublishedPorts {
		c.Ports = append(c.Ports, fmt.Sprintf("127.0.0.1:%d:%d", p, p))
	}
	return c
}

func optionsFor(res Resource, c Config) policy.Options {
	return policy.Options{
		ResourceID: res.Slug, VolumesRoot: c.VolumesRoot, DockerDataDir: c.DockerDataDir,
		Trusted: res.Cfg.Isolation.Trusted, AllowCaps: res.Cfg.Isolation.CapAdd, AllowPublish: len(res.Cfg.PublishedPorts) > 0,
		AppArmor: c.AppArmor,
	}
}

// VolumeHostPath is where a resource's bind volume lives on the host. It is the panel's answer to "where is my
// data", and the only path policy will accept as a bind source for this resource.
func (e *Engine) VolumeHostPath(slug, name string) string {
	return path.Join(e.cfg.VolumesRoot, slug, name)
}

// VolumesRoot is the directory every bind mount of a managed resource must live under.
func (e *Engine) VolumesRoot() string { return e.cfg.VolumesRoot }

// EnsureVolumeDir creates a bind volume's directory, ready to mount. It is idempotent and is called both when the
// volume is created (so the operator learns about a broken host now, not at the next deploy) and before every
// start/release.
//
// The permissions are the point, not an afterthought: the resource tree stays 0751 so no other tenant on this
// host can traverse into it, while the leaf is 0777 because the container mounts it as an arbitrary uid -- the
// image's own user, or a remapped one under user namespaces -- which has no relationship to the panel's user and
// would otherwise hit a read-only directory it was promised.
func (e *Engine) EnsureVolumeDir(slug, name string) error {
	if slug == "" || name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return fmt.Errorf("invalid volume path for %q", name)
	}
	root := filepath.FromSlash(path.Join(e.cfg.VolumesRoot, slug))
	if err := os.MkdirAll(root, 0o751); err != nil {
		return err
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return err
	}
	// MkdirAll applies the umask, and an existing directory keeps whatever mode it was made with. Chmod so the
	// statement above is actually true on hosts that already had the directory.
	return os.Chmod(dir, 0o777)
}

// ImageTag is the local tag a deployment's image is retained under, which is also what a rollback or a restart
// without a rebuild asks Docker for.
func ImageTag(slug, deploymentID string) string { return "codeplo/" + slug + ":" + deploymentID }

// JobKindReap is the durable half of stop and delete: containers (and, for delete, image tags) are removed by a
// retried job, so a slow or absent Docker daemon cannot strand a resource in a half-stopped state.
const JobKindReap = "deploy.reap"

// ReapRequest is the reap job's payload. Images is empty for a stop and holds the resource's retained tags for a delete.
type ReapRequest struct {
	ResourceID string   `json:"resource_id"`
	Slug       string   `json:"slug"`
	Images     []string `json:"images,omitempty"`
}

// EnqueueReap writes the job inside the caller's transaction, so a resource stop/delete and its cleanup work commit
// together: the request cannot be recorded without the containers being scheduled for removal.
func (e *Engine) EnqueueReap(ctx context.Context, tx *sql.Tx, q *jobs.Queue, in ReapRequest) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, err = q.Enqueue(ctx, tx, JobKindReap, string(payload), e.cfg.Now())
	return err
}

// StopResource removes every container of a resource (any state). It is idempotent: a resource with no containers
// stops cleanly, which is what a retry after a partial failure needs.
func (e *Engine) StopResource(ctx context.Context, resourceID string) (int, error) {
	if e.rt == nil {
		return 0, runtime.ErrUnavailable
	}
	states, err := e.rt.ListContainers(ctx, runtime.LabelResource)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range states {
		if s.Labels[runtime.LabelResource] != resourceID {
			continue
		}
		if s.Running {
			if err := e.rt.StopContainer(ctx, s.ID, e.cfg.StopGraceS); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return n, err
			}
		}
		if err := e.rt.RemoveContainer(ctx, s.ID); err != nil && !errors.Is(err, runtime.ErrNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// reap does the job's work: containers first, then the image tags a delete orphaned.
func (e *Engine) reap(ctx context.Context, in ReapRequest) error {
	if _, err := e.StopResource(ctx, in.ResourceID); err != nil {
		return err
	}
	if e.rt != nil {
		for _, ref := range in.Images {
			if err := e.rt.RemoveImage(ctx, ref); err != nil && !errors.Is(err, runtime.ErrNotFound) {
				return err
			}
		}
	}
	e.syncEdge(ctx)
	e.pub("resource.updated", map[string]any{"resource_id": in.ResourceID})
	return nil
}

// RegisterReap installs the reap handler next to the deploy handler (Engine.Register calls it).
func (e *Engine) RegisterReap(q *jobs.Queue) {
	q.Register(JobKindReap, func(ctx context.Context, payload string) error {
		var in ReapRequest
		if err := json.Unmarshal([]byte(payload), &in); err != nil || in.ResourceID == "" {
			return nil // a malformed payload can never succeed; drop it instead of retrying five times
		}
		return e.reap(ctx, in)
	})
}

// CleanupStats is the API's CleanupResult in host-friendly units.
type CleanupStats struct {
	ReclaimedMB       int64 `json:"reclaimed_mb"`
	ImagesRemoved     int   `json:"images_removed"`
	ContainersRemoved int   `json:"containers_removed"`
	BuildCacheMB      int64 `json:"build_cache_mb"`
}

// Cleanup prunes dangling images, stopped managed containers and the build cache. Nothing here looks at user data:
// volume directories are never pruned, so cleanup can be offered to any operator with system:cleanup.
func (e *Engine) Cleanup(ctx context.Context) (CleanupStats, error) {
	if e.rt == nil {
		return CleanupStats{}, runtime.ErrUnavailable
	}
	p, err := e.rt.Prune(ctx)
	if err != nil {
		return CleanupStats{}, err
	}
	mb := func(b int64) int64 { return b / (1 << 20) }
	return CleanupStats{ReclaimedMB: mb(p.ReclaimedBytes), ImagesRemoved: p.ImagesRemoved,
		ContainersRemoved: p.ContainersRemoved, BuildCacheMB: mb(p.BuildCacheBytes)}, nil
}

// RetainImagesFor is the resource's own keep count, or the setting's, or 5 (the same order retention.go uses).
func (e *Engine) RetainImagesFor(ctx context.Context, resourceID string) int {
	return e.retentionCount(ctx, resourceID)
}

// ActiveImageRef returns the reference a restart should reuse: the retained local tag of the resource's active
// deployment. Ok is false when the resource has no active deployment, which is the API's 409 case.
func (e *Engine) ActiveImageRef(ctx context.Context, resourceID string) (ref, depID string, ok bool) {
	var slug, active, digest string
	if err := e.db.QueryRowContext(ctx, `SELECT r.slug, r.active_deployment_id, COALESCE(d.image_digest,'')
		FROM resources r LEFT JOIN deployments d ON d.id=r.active_deployment_id WHERE r.id=?`, resourceID).
		Scan(&slug, &active, &digest); err != nil {
		return "", "", false
	}
	if strings.TrimSpace(active) == "" {
		return "", "", false
	}
	return ImageTag(slug, active), active, true
}

// ResourceTags lists the local image tags of a resource, newest first: what a delete hands to the reap job so the
// images do not outlive the rows that named them.
func (e *Engine) ResourceTags(ctx context.Context, resourceID string) []string {
	rows, err := e.db.QueryContext(ctx, `SELECT r.slug, d.id FROM resources r JOIN deployments d ON d.resource_id=r.id
		WHERE r.id=? AND d.image_digest IS NOT NULL ORDER BY d.started_at DESC, d.id DESC`, resourceID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug, id string
		if rows.Scan(&slug, &id) == nil {
			out = append(out, ImageTag(slug, id))
		}
	}
	return out
}
