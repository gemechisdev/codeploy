// Package runtime is the container-runtime port (plan 4.3, internal/modules/runtime/): the terminal's
// container shell and the resource log stream both compile against Runtime, so the API layer never touches
// the Docker Engine API. The Docker implementation lives in docker.go; tests substitute a fake.
package runtime

import (
	"context"
	"errors"
	"io"
	"time"
)

// LabelResource is the container label that binds a container to the panel resource it serves. The runtime
// resolves "the container for resource X" from this label alone, which keeps P3 working before the
// resources table arrives in P4. P4 must stamp it on every container it creates.
const LabelResource = "codeplo.resource"

var (
	// ErrUnavailable means no container runtime is reachable (absent or dead socket). Callers map it to a
	// 500/internal error, never to a user-facing 404: the resource may be fine, the host is not.
	ErrUnavailable = errors.New("container runtime unavailable")
	// ErrNoContainer means the runtime answered but no running container carries that resource label.
	// Callers map it to 404/409 depending on the route.
	ErrNoContainer = errors.New("no running container for this resource")
)

// Container is what the panel needs to know about one managed container.
type Container struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Running bool   `json:"running"`
	// ResourceID is the value of LabelResource; empty when the container is not panel-managed.
	ResourceID string `json:"resource_id"`
}

// LogOptions selects which log lines a stream starts from. Zero values mean "runtime default": Follow false
// stops at the current end, Tail 0 lets the runtime pick, Since zero time applies no lower bound.
type LogOptions struct {
	Tail   int
	Since  time.Time
	Follow bool
}

// LogLine is one container log line with its runtime-recorded timestamp.
type LogLine struct {
	Time   time.Time `json:"at"`
	Stream string    `json:"stream"` // "stdout" or "stderr"
	Line   string    `json:"line"`
}

// LogFunc consumes streamed lines in order. Returning an error stops the stream and is reported back from
// Logs: that is the backpressure hook, so a slow consumer cancels the upstream read instead of buffering
// without bound.
type LogFunc func(LogLine) error

// ExecStream is an attached container exec: the caller writes input, reads merged output, resizes the tty,
// and waits for the exit code. Close terminates the exec and releases the connection; it is safe to call
// more than once.
type ExecStream interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Resize(cols, rows int) error
	Wait(ctx context.Context) (int, error)
	Close() error
}

// Runtime is the frozen P3 port. Implementations must be safe for concurrent use.
type Runtime interface {
	// Available reports whether the runtime answers, returning ErrUnavailable when it does not.
	Available(ctx context.Context) error
	// Resolve finds the container a resource ID maps to via LabelResource. It returns ErrNoContainer when
	// nothing running carries the label, so a stopped resource cannot be shelled into or tailed.
	Resolve(ctx context.Context, resourceID string) (Container, error)
	// Logs streams container logs, blocking until the options are exhausted, ctx is cancelled or emit errs.
	Logs(ctx context.Context, container string, opt LogOptions, emit LogFunc) error
	// Exec creates and attaches a shell in the container. cols/rows are the initial tty size and only apply
	// when tty is true.
	Exec(ctx context.Context, container string, cmd []string, tty bool, cols, rows int) (ExecStream, error)
}
