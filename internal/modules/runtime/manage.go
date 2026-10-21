package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/codeplo/codeplo/internal/policy"
)

// Labels the deploy engine stamps on everything it creates. LabelResource (runtime.go) is the contract the
// terminal and the log stream resolve containers by; LabelDeployment is what boot recovery and cancel clean up by.
const (
	LabelDeployment = "codeplo.deployment"
	LabelManaged    = "codeplo.managed"
	LabelRole       = "codeplo.role" // "app" | "release"
	RoleApp         = "app"
	RoleRelease     = "release"
)

// ErrNotFound is returned by Manager calls when the container or image does not exist. Removal is idempotent
// and never returns it.
var ErrNotFound = errors.New("not found")

// State is the inspected state of one managed container.
type State struct {
	ID         string
	Name       string
	Image      string
	Status     string // created | running | exited | ...
	Running    bool
	ExitCode   int
	OOMKilled  bool
	Health     string // "" when the image defines no healthcheck
	IP         string // address on the requested network
	Labels     map[string]string
	StartedAt  time.Time
	FinishedAt time.Time
}

// ImageInfo is what the engine records about a built or pulled image.
type ImageInfo struct {
	ID          string // sha256:...
	RepoDigests []string
	SizeBytes   int64
}

// PruneResult mirrors the OpenAPI CleanupResult.
type PruneResult struct {
	ReclaimedBytes    int64
	ImagesRemoved     int
	ContainersRemoved int
	BuildCacheBytes   int64
}

// Manager is the P4 container-lifecycle port. It extends the frozen P3 Runtime port rather than changing it.
// Nothing here relies on interrupting an exec (measured: this daemon cannot kill one): cancellation is the
// container lifecycle (Stop/Kill/Remove, by label) plus context cancellation and per-step timeouts.
//
// CreateContainer takes a policy.Approved, which only policy.Enforce can build, so a container that violates
// the isolation policy cannot be created through this port.
type Manager interface {
	Runtime
	EnsureNetwork(ctx context.Context, name string) error
	CreateContainer(ctx context.Context, a policy.Approved) (id string, err error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, graceSeconds int) error
	KillContainer(ctx context.Context, id string) error
	RemoveContainer(ctx context.Context, id string) error
	InspectContainer(ctx context.Context, id, network string) (State, error)
	// ListContainers lists containers (any state) that carry the label key, e.g. LabelDeployment.
	ListContainers(ctx context.Context, labelKey string) ([]State, error)
	// WaitContainer blocks until the container is not running and returns its exit code. It honours ctx.
	WaitContainer(ctx context.Context, id string) (int, error)
	ImageInspect(ctx context.Context, ref string) (ImageInfo, error)
	TagImage(ctx context.Context, src, repo, tag string) error
	RemoveImage(ctx context.Context, ref string) error
	// Prune removes dangling images, stopped containers labelled codeplo.managed and build cache.
	Prune(ctx context.Context) (PruneResult, error)
}
