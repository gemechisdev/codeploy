// Package databases is Phase 6's database knowledge: which resources are databases, how each engine's dump is
// produced, and how a dump is put back. Everything the REST surface of backups does is here, and nothing here
// knows about HTTP.
//
// Three rules shape it. A dump password travels in the exec's environment, never in argv, because argv is what the
// panel logs and what `Inspect` shows. A backup is only successful once its size and checksum have been verified
// against what was written, not against what was intended. And a restore always takes a snapshot of the current
// state first, because the operator's next question after a restore that made things worse is "what was it before".
package databases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/objectstore"
)

// Ops is the container surface a backup needs. It is runtime.Manager plus the three methods the dump and the
// restore use, so *runtime.Docker satisfies it and a test fixture can too. The exec and archive methods are the
// ones Phase 6 added: a dump that puts its password in argv would be a leaked secret on every scheduled backup.
type Ops interface {
	ListContainers(ctx context.Context, labelKey string) ([]runtime.State, error)
	StopContainer(ctx context.Context, id string, graceSeconds int) error
	StartContainer(ctx context.Context, id string) error
	ExecWithEnv(ctx context.Context, container string, cmd, env []string) (runtime.ExecStream, error)
	CopyFileOut(ctx context.Context, container, filePath string, maxBytes int64) ([]byte, error)
	CopyFileIn(ctx context.Context, container, dirPath, name string, mode os.FileMode, content []byte) error
}

// Service is the package's entry point.
type Service struct {
	DB  *sql.DB
	Key []byte
	// Runtime is nil on a host without Docker, which makes every backup fail with a stated reason rather than
	// panic: the panel still has to show the schedule and its history.
	Runtime Ops
	// DataDir is the panel's state directory; dumps land under <DataDir>/backups.
	DataDir string
	// S3 is the configured object destination, or nil. It comes from the daemon's environment, never from the
	// database or an API request, because a bucket secret in a plaintext settings table is the credential the
	// threat model says must not exist.
	S3 *objectstore.Client
	// Timeout bounds one dump or restore.
	Timeout time.Duration
	// MaxBytes bounds a file-based dump on its way through the panel. The archive port hands back bytes rather than
	// a stream, so an unbounded RDB is an unbounded allocation on a host that has maybe a gigabyte to spare; a dump
	// over the limit fails with a stated size instead of an out-of-memory kill. Defaults to 1 GiB.
	MaxBytes int64
	Now      func() time.Time
}

func New(db *sql.DB, key []byte) *Service {
	return &Service{DB: db, Key: key, Timeout: 15 * time.Minute}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) ts(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

func (s *Service) id(prefix string) string { return crypto.ULID(prefix, s.now().UnixMilli()) }

func (s *Service) timeout() time.Duration {
	if s.Timeout <= 0 {
		return 15 * time.Minute
	}
	return s.Timeout
}

func (s *Service) maxBytes() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return 1 << 30
}

// nullStr turns a scanned column into the JSON null the contract allows.
func nullStr(s sql.NullString) *string {
	if !s.Valid {
		return nil
	}
	return &s.String
}

// parseTS reads back a timestamp this package wrote, which is the only way to name a payload from a run row the
// daemon loaded after a restart.
func (s *Service) parseTS(text string) (time.Time, error) {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", text)
	if err != nil {
		return time.Time{}, fmt.Errorf("backup row carries an unreadable timestamp %q: %w", text, err)
	}
	return t.UTC(), nil
}

// Root is where local dumps live.
func (s *Service) Root() string {
	dir := s.DataDir
	if dir == "" {
		dir = "/var/lib/codeplo"
	}
	return filepath.Join(dir, "backups")
}

// FieldErrors, NotFound and Conflict are the module's three failure shapes, as in the other P6 packages.
type FieldErrors map[string]string

func (e FieldErrors) Error() string { return "validation failed" }

type NotFound struct{ What string }

func (e NotFound) Error() string { return e.What + " not found" }

type Conflict struct{ Msg string }

func (e Conflict) Error() string { return e.Msg }

// Engine is one database kind's backup contract.
//
// Dump and restore are separate because the engines do not agree on how bytes move: Postgres and MySQL write a SQL
// dump to the exec's stdout and read one back from its stdin, while Redis has no stdout dump at all — it saves an
// RDB file inside the container, which is copied out, and it restores by having that file put back and the
// container restarted, because a running Redis will not load a file over what it is already serving.
type Engine interface {
	Key() string
	Name() string
	// Port is the engine's listen port, which is what the panel puts in a resource's config and what a dump
	// command connects to from inside the container.
	Port() int
	// DumpPlan is the command that produces the dump. For a stdout engine the bytes arrive on the exec's stdout;
	// for a file engine the command writes ContainerPath and the caller copies it out afterwards.
	DumpPlan(ctx context.Context, r Resource) (Dump, error)
	// RestorePlan is the way back. For a stdin engine it is a command fed by the dump file; for a file engine it is
	// the path the file is copied to, and Restart says the container has to cycle for the load to happen. d is the
	// dump being restored, which a file engine needs for its payload name.
	RestorePlan(ctx context.Context, r Resource, d Dump) (Restore, error)
}

// Dump describes one engine's dump.
type Dump struct {
	// Stdout is true when the command's stdout carries the bytes; false when it writes ContainerPath instead.
	Stdout bool
	Cmd    []string
	Env    []string
	// ContainerPath is the in-container file for a file dump, and the in-container directory it lives in is where
	// a file restore writes it.
	ContainerPath string
	// Ext is the extension the payload gets: ".sql.gz" for the text engines, ".rdb" for Redis.
	Ext string
}

// Restore describes one engine's restore step.
type Restore struct {
	// Stdin, when set, is the command that consumes the dump from its standard input.
	Stdin []string
	Env   []string
	// CopyTo is the container path the payload is written to for a file restore.
	CopyTo string
	// Restart cycles the container after a file restore.
	Restart bool
}

// enginesByName is the registry Detect consults. The keys are the template keys the UI lists, so a resource
// deployed from a template and a resource deployed by hand from the same image get the same treatment.
var enginesByName = map[string]Engine{}

func register(e Engine) { enginesByName[e.Key()] = e }

// Detect names the engine a resource's image is.
//
// The match is on the repository's last path segment, so `docker.io/library/postgres:16`, `postgres:16-alpine` and
// `ghcr.io/org/mirror-postgres:16` all read as Postgres while `postgresql-exporter` does not. That is a heuristic
// and it is documented as one: the panel cannot introspect an arbitrary image, and the alternative — a database
// resource whose engine is unknown — has to fail a backup anyway.
func Detect(imageRef string) (Engine, bool) {
	repo := imageRef
	if i := strings.IndexAny(repo, ":@"); i >= 0 {
		repo = repo[:i]
	}
	last := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		last = repo[i+1:]
	}
	last = strings.ToLower(strings.TrimSuffix(last, ".docker"))
	for _, e := range enginesByName {
		for _, alias := range engineAliases[e.Key()] {
			if last == alias || strings.HasPrefix(last, alias+"-") || strings.HasSuffix(last, "-"+alias) {
				return e, true
			}
		}
	}
	return nil, false
}

// engineAliases is what each engine answers to. mysql owns mariadb because mysqldump speaks to it, and the
// dump-and-restore contract is the same even though the image is not.
var engineAliases = map[string][]string{
	"postgres": {"postgres", "postgresql"},
	"mysql":    {"mysql", "mariadb"},
	"redis":    {"redis", "valkey"},
}

// Resource is a database resource resolved for a backup: its row, its config, and the engine the panel will use.
type Resource struct {
	ID     string
	Name   string
	Slug   string
	Config deploy.ResourceConfig
	Engine Engine
	// Vars is the decrypted runtime environment, which is where the dump credentials live.
	Vars []deploy.EnvVar
}

// Lookup resolves a resource and refuses one this panel cannot dump, with the reason the operator can act on.
func (s *Service) Lookup(ctx context.Context, q Querier, resourceID string) (Resource, error) {
	var (
		r      Resource
		cfgRaw string
	)
	err := q.QueryRowContext(ctx, `SELECT id, name, slug, config_json FROM resources WHERE id=?`, resourceID).
		Scan(&r.ID, &r.Name, &r.Slug, &cfgRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return Resource{}, NotFound{What: "resource"}
	}
	if err != nil {
		return Resource{}, err
	}
	cfg, err := deploy.ParseConfig(cfgRaw)
	if err != nil {
		return Resource{}, fmt.Errorf("resource %s has unreadable config: %w", r.ID, err)
	}
	r.Config = cfg
	ref := ""
	if cfg.Image.Ref != nil {
		ref = *cfg.Image.Ref
	}
	e, ok := Detect(ref)
	if !ok {
		return Resource{}, Conflict{Msg: fmt.Sprintf("No backup engine is known for image %q: backups cover PostgreSQL, MySQL and Redis images", ref)}
	}
	r.Engine = e
	vars, err := deploy.LoadEnv(ctx, s.DB, s.Key, r.ID)
	if err != nil {
		return Resource{}, err
	}
	r.Vars = vars
	return r, nil
}

// runtimeVar returns a resource's runtime variable, or the fallback when it has none. A database deployed without
// POSTGRES_USER still runs as postgres, and a dump that guessed wrong fails as an authentication error far from
// here, so the defaults are the image's own.
func (r Resource) runtimeVar(key, fallback string) string {
	for _, v := range r.Vars {
		if !v.Runtime() {
			continue
		}
		if v.Key == key && strings.TrimSpace(v.Value) != "" {
			return v.Value
		}
	}
	return fallback
}

// firstVar returns the first of several names that is set, which is how Postgres is happy with either
// POSTGRES_USER or the custom variable a hand-written compose file uses.
func (r Resource) firstVar(fallback string, keys ...string) string {
	for _, k := range keys {
		if v := r.runtimeVar(k, ""); v != "" {
			return v
		}
	}
	return fallback
}

// port is where the engine listens inside its own container: the resource's configured port when it has one, which
// is what a non-standard deployment needs, and the engine's default otherwise.
func (r Resource) port() int {
	if r.Config.Port > 0 {
		return r.Config.Port
	}
	if r.Engine != nil {
		return r.Engine.Port()
	}
	return 0
}

// container is the running container a resource's data lives in.
//
// The active deployment's container id is the answer, and the managed label is the fallback: a resource whose row
// says it is running something is running something, and a backup that refused to start because one bookkeeping
// column was stale would punish the operator for a bug in the panel.
func (s *Service) container(ctx context.Context, r Resource) (string, error) {
	if s.Runtime == nil {
		return "", errors.New("no container runtime on this host")
	}
	var id string
	err := s.DB.QueryRowContext(ctx, `SELECT d.container_id FROM deployments d JOIN resources res ON res.active_deployment_id=d.id WHERE res.id=?`, r.ID).Scan(&id)
	if err == nil && strings.TrimSpace(id) != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	states, err := s.Runtime.ListContainers(ctx, runtime.LabelResource)
	if err != nil {
		return "", err
	}
	for _, st := range states {
		if st.Labels[runtime.LabelResource] == r.ID && st.Running && st.Labels[runtime.LabelRole] != runtime.RoleRelease {
			return st.ID, nil
		}
	}
	return "", Conflict{Msg: fmt.Sprintf("%s has no running container to back up", r.Name)}
}

// Querier is satisfied by *sql.DB and *sql.Tx, as in the other P6 packages.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// runExec runs one command in a container and returns its exit code, streaming stdout into out and keeping a
// bounded tail of stderr for the error.
//
// stdin is written from a goroutine and closed when the copy finishes: a restore command waits for EOF before it
// commits, so a caller that never closed it would hang until the timeout, and a caller that closed it before the
// bytes were written would truncate the dump.
func (s *Service) runExec(ctx context.Context, container string, cmd, env []string, stdin io.Reader, out io.Writer) (int, string, error) {
	stream, err := s.Runtime.ExecWithEnv(ctx, container, cmd, env)
	if err != nil {
		return -1, "", err
	}
	defer stream.Close()

	// The output copy blocks until the command exits, and nothing in the runtime port aborts a hijacked exec on a
	// cancelled context, so a dump that hangs would outlive its own timeout. Closing the stream on cancellation is
	// what makes the copy below return instead.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-done:
		}
	}()

	errBuf := newTail(4096)
	if stdin != nil {
		go func() {
			_, _ = io.Copy(stream.Stdin(), stdin)
			_ = stream.Stdin().Close()
		}()
	}
	if _, err := runtime.CopyDemuxed(stream.Stdout(), out, errBuf); err != nil {
		return -1, errBuf.String(), err
	}
	code, err := stream.Wait(ctx)
	if err != nil {
		return -1, errBuf.String(), err
	}
	return code, errBuf.String(), nil
}

// tail keeps the last n bytes written to it, which is how a dump tool's error is reported without pulling a whole
// log into memory or into an audit row.
type tail struct {
	n   int
	buf []byte
}

func newTail(n int) *tail { return &tail{n: n} }

func (t *tail) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.n {
		t.buf = t.buf[len(t.buf)-t.n:]
	}
	return len(p), nil
}

func (t *tail) String() string {
	s := strings.TrimSpace(string(t.buf))
	if s == "" {
		return ""
	}
	slog.Debug("command wrote to stderr", "stderr", s)
	return s
}
