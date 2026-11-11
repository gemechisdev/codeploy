package deploy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/codeplo/codeplo/internal/audit"
	"github.com/codeplo/codeplo/internal/crypto"
	"github.com/codeplo/codeplo/internal/jobs"
	"github.com/codeplo/codeplo/internal/modules/deploy/builders"
	"github.com/codeplo/codeplo/internal/modules/git/providers"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/policy"
	"github.com/codeplo/codeplo/internal/store"
)

// StepTimeouts bound every step (plan 3.8 "hung steps"). A step that overruns fails the deployment and cleans up.
type StepTimeouts struct {
	Preflight, Pull, Build, Release, Start, Switch time.Duration
}

// Config holds the engine's tunables and the probes tests replace.
type Config struct {
	WorkDir       string // clones and scratch; created 0700
	VolumesRoot   string // /var/lib/codeplo/volumes
	DockerDataDir string // /var/lib/docker
	AppArmor      bool

	Steps          StepTimeouts
	Observe        time.Duration // 0: read settings.observation_window_s (default 60 s)
	ObserveEvery   time.Duration // poll interval inside the window (default: health interval, max 5 s)
	FailThreshold  int           // consecutive failed checks that trigger automatic rollback (default 3)
	HeartbeatEvery time.Duration
	DiskWatchEvery time.Duration
	StopGraceS     int

	Disk         func(path string) (free, total uint64, err error)
	MemAvailable func() (uint64, error)
	Probe        func(ctx context.Context, url string, timeout time.Duration) error
	// ProbeTCP answers the health of a resource that serves a port instead of a path — a database. `hostport` is
	// the same container address the HTTP probe's URL is built from.
	ProbeTCP func(ctx context.Context, hostport string, timeout time.Duration) error
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error
}

// DefaultConfig fills every unset field. dataDir is the panel's data directory.
func (c Config) withDefaults(dataDir string) Config {
	if c.WorkDir == "" {
		c.WorkDir = dataDir + "/work"
	}
	if c.VolumesRoot == "" {
		c.VolumesRoot = dataDir + "/volumes"
	}
	if c.DockerDataDir == "" {
		c.DockerDataDir = "/var/lib/docker"
	}
	s := &c.Steps
	def := func(p *time.Duration, d time.Duration) {
		if *p <= 0 {
			*p = d
		}
	}
	def(&s.Preflight, 2*time.Minute)
	def(&s.Pull, 10*time.Minute)
	def(&s.Build, 30*time.Minute)
	def(&s.Release, 10*time.Minute)
	def(&s.Start, 2*time.Minute)
	def(&s.Switch, time.Minute)
	if c.FailThreshold <= 0 {
		c.FailThreshold = 3
	}
	def(&c.HeartbeatEvery, 5*time.Second)
	def(&c.DiskWatchEvery, 5*time.Second)
	if c.StopGraceS <= 0 {
		c.StopGraceS = 10
	}
	if c.Disk == nil {
		c.Disk = DiskFree
	}
	if c.MemAvailable == nil {
		c.MemAvailable = MemAvailable
	}
	if c.Probe == nil {
		c.Probe = httpProbe
	}
	if c.ProbeTCP == nil {
		c.ProbeTCP = tcpProbe
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = sleepCtx
	}
	return c
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func httpProbe(ctx context.Context, url string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return nil
}

// tcpProbe is the health check of a resource that serves a port rather than a path. Postgres, MySQL and Redis answer
// their own protocols, which the panel does not speak, so accepting a connection is what "this container is serving"
// means for them. The databases module has the protocol-side health (it runs the engine's own client tools); the
// engine only needs to know whether anything is listening.
func tcpProbe(ctx context.Context, hostport string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", hostport)
	if err != nil {
		return fmt.Errorf("%s is not accepting connections: %w", hostport, err)
	}
	_ = c.Close()
	return nil
}

// Deps are the engine's collaborators.
type Deps struct {
	DB      *sql.DB
	Key     []byte
	Runtime runtime.Manager
	Builder builders.Builder
	// Edge re-renders the Caddy config from the database. Best effort: if Caddy is down the supervisor loop repairs it.
	Edge    func(context.Context) error
	Publish func(name string, data any)
	// PreDeploy snapshots the resource's database before a new version can touch it. nil when the databases module
	// is off, which the step below reports in the deployment log and then passes — a panel that cannot deploy
	// because an optional module is switched off would be a worse failure than one without the safety net.
	// It returns whether a snapshot was taken, so the log says which of the two happened.
	PreDeploy func(ctx context.Context, resourceID string) (bool, error)
	// GitHubToken mints a short-lived clone credential for a repository the panel's GitHub App installation covers,
	// and says whether this App is the thing that should be covering it. nil when the daemon was started without
	// CODEPLO_GITHUB_APP_*, which leaves every source authenticating with what the resource itself carries.
	//
	// It is consulted only for a source with no deploy key and no stored token, so an App installation can never
	// quietly take over a repository the operator gave their own credential to.
	GitHubToken func(ctx context.Context, repoURL string) (string, bool, error)
	// ReportStatus publishes one commit status to the forge a Git source came from (plan G7). It is best effort by
	// contract: an unreachable forge never fails a deployment, because by the time the panel reports, the deploy has
	// already happened. nil means nothing is told and no request is ever made — which is what a daemon without a
	// GitHub App passes.
	ReportStatus func(ctx context.Context, s providers.CommitStatus) error
	DataDir      string
	Config       Config
}

// Engine runs deployments. One Engine per daemon; the queue's single worker gives build concurrency = 1.
type Engine struct {
	db  *sql.DB
	key []byte
	rt  runtime.Manager
	b   builders.Builder
	edg func(context.Context) error
	pub func(string, any)
	// preDeploy is Deps.PreDeploy; see the note there for what nil means.
	preDeploy func(context.Context, string) (bool, error)
	// ghToken is Deps.GitHubToken; nil means no GitHub App is configured on this daemon.
	ghToken func(context.Context, string) (string, bool, error)
	// reportStatus is Deps.ReportStatus; nil means the panel tells no forge how its deploys ended.
	reportStatus func(context.Context, providers.CommitStatus) error
	cfg          Config
	// rawSteps keeps the timeouts the caller set explicitly. withDefaults fills every zero value, so without it
	// the engine could not tell "the tests asked for this bound" from "nobody asked" and would ignore settings.
	rawSteps StepTimeouts

	mu      sync.Mutex
	running map[string]context.CancelCauseFunc
}

func New(d Deps) *Engine {
	pub := d.Publish
	if pub == nil {
		pub = func(string, any) {}
	}
	return &Engine{db: d.DB, key: d.Key, rt: d.Runtime, b: d.Builder, edg: d.Edge, pub: pub,
		preDeploy: d.PreDeploy, ghToken: d.GitHubToken, reportStatus: d.ReportStatus,
		cfg: d.Config.withDefaults(d.DataDir), rawSteps: d.Config.Steps,
		running: map[string]context.CancelCauseFunc{}}
}

// timeouts resolves the per-step bounds: an explicit Config value wins, otherwise the seeded setting does, so an
// operator can lengthen a build without rebuilding the daemon.
func (e *Engine) timeouts(ctx context.Context) StepTimeouts {
	s := e.cfg.Steps
	from := func(raw *time.Duration, key string) {
		if *raw > 0 {
			return
		}
		if v := e.settingInt(ctx, key, int(*raw)); time.Duration(v)*time.Second >= time.Minute {
			*raw = time.Duration(v) * time.Second
		}
	}
	from(&s.Build, "deploy.build_timeout_s")
	from(&s.Release, "deploy.release_timeout_s")
	return s
}

// Sentinel causes.
var (
	errCancelled  = errors.New("deployment cancelled")
	errRolledBack = errors.New("rolled back automatically")
	errDiskLow    = errors.New("free disk space fell below the safety floor during the step")
	// errSettled means "the deployment's own row already reports what happened, and its container is in the state
	// the route needs". finish must write nothing: it is returned when the engine could not record a transition it
	// intended, so the panel shows the last state it actually holds rather than one the engine guessed.
	errSettled = errors.New("deployment state already recorded")
)

// StepError is a failed step with a message fit for the deployment's error field.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

func fail(step, format string, a ...any) error {
	return &StepError{Step: step, Err: fmt.Errorf(format, a...)}
}

// Register installs the queue handler. Handlers must be idempotent (the queue re-runs jobs left 'running' by a
// crash): Run is a no-op for any deployment that is not 'queued', and recovery has already marked the in-flight
// ones interrupted by the time the queue starts.
func (e *Engine) Register(q *jobs.Queue) {
	e.RegisterReap(q)
	q.Register(JobKind, func(ctx context.Context, payload string) error {
		var p struct {
			DeploymentID string `json:"deployment_id"`
		}
		if err := json.Unmarshal([]byte(payload), &p); err != nil || p.DeploymentID == "" {
			return nil // a malformed payload can never succeed; drop it instead of retrying five times
		}
		e.Run(ctx, p.DeploymentID)
		return nil // the outcome lives in deployments.status; returning an error would make the queue retry
	})
}

// NewDeployment is the input to Create.
type NewDeployment struct {
	ResourceID string
	Trigger    string
	Mode       string
	Branch     string
	ImageRef   string
	RollbackOf string
}

// Create inserts a queued deployment and its job inside the caller's transaction, superseding any older queued
// deployment of the same resource (plan: a newer trigger supersedes queued, never running, ones).
func (e *Engine) Create(ctx context.Context, tx *sql.Tx, q *jobs.Queue, in NewDeployment) (string, error) {
	now := ts(e.cfg.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE deployments SET status='cancelled', step='superseded', finished_at=?, error='Superseded by a newer deployment' WHERE resource_id=? AND status='queued'`, now, in.ResourceID); err != nil {
		return "", err
	}
	id := lower(crypto.ULID("dep_", e.cfg.Now().UnixMilli()))
	var rb any
	if in.RollbackOf != "" {
		rb = in.RollbackOf
	}
	var br any
	if in.Branch != "" {
		br = in.Branch
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO deployments(id,resource_id,status,step,trigger,mode,branch,image_ref,started_at,rollback_of) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		id, in.ResourceID, StQueued, "queued", in.Trigger, in.Mode, br, in.ImageRef, now, rb); err != nil {
		return "", err
	}
	payload, _ := json.Marshal(map[string]string{"deployment_id": id})
	if _, err := q.Enqueue(ctx, tx, JobKind, string(payload), e.cfg.Now()); err != nil {
		return "", err
	}
	return id, nil
}

// Cancel requests cancellation. A queued deployment is cancelled immediately; a running one has its context
// cancelled, every step checks it, and cleanup is label-driven and idempotent. The returned status is the
// deployment's status right after the request.
func (e *Engine) Cancel(ctx context.Context, id string) (string, error) {
	now := ts(e.cfg.Now())
	res, err := e.db.ExecContext(ctx, `UPDATE deployments SET status='cancelled', step='cancelled', finished_at=?, error='Cancelled before it started' WHERE id=? AND status='queued'`, now, id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		e.emit(id)
		return StCancelled, nil
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE deployments SET cancel_requested=1 WHERE id=? AND status IN ('preparing','building','starting','verifying')`, id); err != nil {
		return "", err
	}
	e.mu.Lock()
	cancel := e.running[id]
	e.mu.Unlock()
	if cancel != nil {
		cancel(errCancelled)
	}
	d, err := loadDep(ctx, e.db, id)
	return d.Status, err
}

func (e *Engine) track(id string, c context.CancelCauseFunc) {
	e.mu.Lock()
	e.running[id] = c
	e.mu.Unlock()
}
func (e *Engine) untrack(id string) {
	e.mu.Lock()
	delete(e.running, id)
	e.mu.Unlock()
}

// emit publishes the deployment's current status; best effort.
func (e *Engine) emit(id string) {
	var rid, st, step string
	if err := e.db.QueryRow(`SELECT resource_id, status, step FROM deployments WHERE id=?`, id).Scan(&rid, &st, &step); err != nil {
		return
	}
	e.pub("deployment.updated", map[string]any{"deployment_id": id, "resource_id": rid, "status": st, "step": step})
	e.pub("resource.updated", map[string]any{"resource_id": rid})
}

// run is the state of one execution.
type run struct {
	e    *Engine
	id   string
	dep  Dep
	res  Resource
	logw *logWriter
	red  *Redactor
	env  []EnvVar
	vols []Volume

	pf       PreflightResult
	network  string
	tag      string           // codeplo/<slug>:<deployment>
	name     string           // container name
	spec     policy.Container // pre-policy container for the app
	flags    []string
	newCID   string // new container id
	upstream string
	switched bool
	prev     Dep // the deployment that was active before the switch, when any
	buildMem int64
	// composeDone means acquire already brought the stack up with docker compose; start() only resolves upstream.
	composeDone bool
	// listenPort overrides Cfg.Port for this deployment when auto-detect picked a different listen port (e.g. nginx 8080).
	listenPort int
}

// Run executes one deployment end to end. It returns nothing: the outcome is the deployment's row.
func (e *Engine) Run(ctx context.Context, id string) {
	now := ts(e.cfg.Now())
	res, err := e.db.ExecContext(ctx, `UPDATE deployments SET status='preparing', step='preflight', heartbeat_at=? WHERE id=? AND status='queued' AND cancel_requested=0`, now, id)
	if err != nil {
		slog.Warn("deploy: claim failed", "deployment", id, "err", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // terminal, cancelled, or already running: idempotent no-op
	}
	e.emit(id)
	dep, err := loadDep(ctx, e.db, id)
	if err != nil {
		return
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	e.track(id, cancel)
	defer e.untrack(id)

	hbDone := make(chan struct{})
	go e.heartbeat(runCtx, id, cancel, hbDone)

	r := &run{e: e, id: id, dep: dep}
	rerr := r.execute(runCtx)
	cancel(nil) // stop the heartbeat
	<-hbDone

	if ctx.Err() != nil {
		// The daemon is shutting down mid-deployment. Leave the row where it is: boot recovery marks it
		// interrupted and removes its containers, exactly as after a crash.
		return
	}
	e.finish(context.WithoutCancel(ctx), r, rerr)
}

func (e *Engine) heartbeat(ctx context.Context, id string, cancel context.CancelCauseFunc, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(e.cfg.HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var cr int
			_ = e.db.QueryRow(`UPDATE deployments SET heartbeat_at=? WHERE id=? RETURNING cancel_requested`, ts(e.cfg.Now()), id).Scan(&cr)
			if cr == 1 { // a cancel recorded by another path (the API sets the flag as well as the in-memory cancel)
				cancel(errCancelled)
			}
		}
	}
}

// setStep records the state-machine position. It never writes a terminal deployment. `step` is written as the
// state name (the deployments timeline in the UI indexes on it, and the executable mock uses step == status); the
// prose label of the action inside that state goes to the deployment log instead.
func (r *run) setStep(ctx context.Context, status, label string) error {
	res, err := r.e.db.ExecContext(context.WithoutCancel(ctx), `UPDATE deployments SET status=?, step=?, heartbeat_at=? WHERE id=? AND status NOT IN ('succeeded','failed','cancelled','interrupted','rolled_back')`,
		status, status, ts(r.e.cfg.Now()), r.id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("deployment is no longer running")
	}
	r.logw.Sys("▶ " + label)
	r.e.emit(r.id)
	return nil
}

// do runs one step under its timeout and translates its failure.
func (r *run) do(ctx context.Context, status, step string, timeout time.Duration, fn func(ctx context.Context, cancelStep context.CancelCauseFunc) error) error {
	if r.logw == nil { // first step: the log writer needs the redactor, which prepare builds; use a plain one until then
		r.logw = newLogWriter(r.e.db, r.id, nil, 0, r.e.pub, r.e.cfg.Now)
	}
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	if err := r.setStep(ctx, status, step); err != nil {
		return err
	}
	sctx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)
	tctx, cancelT := context.WithTimeout(sctx, timeout)
	defer cancelT()
	err := fn(tctx, cancelCause)
	if err == nil {
		return nil
	}
	cause := context.Cause(tctx)
	switch {
	case errors.Is(cause, errCancelled) || errors.Is(err, errCancelled):
		return errCancelled
	case errors.Is(cause, errDiskLow):
		free, total, _ := r.e.cfg.Disk(r.e.cfg.WorkDir)
		return fail(step, "The disk filled up during %s (%.1f GB free of %.1f GB). Free space (Settings → cleanup, or remove unused images), then redeploy.", step, float64(free)/float64(gib), float64(total)/float64(gib))
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		return fail(step, "The %s step timed out after %s and was stopped. Check the build or release command for a hang.", step, timeout)
	}
	var se *StepError
	if errors.As(err, &se) {
		return err
	}
	if errors.Is(err, builders.ErrNoSpace) {
		return fail(step, "%s", err.Error())
	}
	if errors.Is(err, builders.ErrOutOfMemory) {
		return fail(step, "%s. An on-host build is capped at half the memory free at pre-flight; a build that needs more wants a larger host or a prebuilt image.", err.Error())
	}
	if errors.Is(err, builders.ErrSourceTooLarge) {
		return fail(step, "%s. Raise the limit in settings, or build the image in CI and deploy it with a hook.", err.Error())
	}
	return &StepError{Step: step, Err: err}
}

func (r *run) execute(ctx context.Context) error {
	e := r.e
	res, err := loadResource(ctx, e.db, r.dep.ResourceID)
	if err != nil {
		return fmt.Errorf("resource not found: %w", err)
	}
	r.res = res
	st := e.timeouts(ctx)
	if err := r.do(ctx, StPreparing, "preflight", st.Preflight, r.prepare); err != nil {
		return err
	}
	acquireTimeout := st.Pull
	if r.dep.Mode == "build" && !r.dep.ReusesImage() {
		acquireTimeout = st.Build
	}
	if err := r.do(ctx, StBuilding, "build", acquireTimeout, r.acquire); err != nil {
		return err
	}
	// Before the release command, not before the switch: a release command runs migrations, and the point of a
	// pre-deploy snapshot is that the data the *previous* version was serving is on disk before anything new can
	// change it.
	if err := r.do(ctx, StStarting, "backup", r.backupTimeout(ctx), r.preDeployBackup); err != nil {
		return err
	}
	if len(r.res.Cfg.ReleaseCommand) > 0 {
		if err := r.do(ctx, StStarting, "release", st.Release, r.release); err != nil {
			return err
		}
	}
	if err := r.do(ctx, StStarting, "start", st.Start, r.start); err != nil {
		return err
	}
	if err := r.do(ctx, StVerifying, "health", r.verifyTimeout(), r.verify); err != nil {
		return err
	}
	if err := r.do(ctx, StSwitching, "switch", st.Switch, r.switchTraffic); err != nil {
		return err
	}
	return r.observe(ctx)
}

// finish writes the terminal state for a deployment whose execution ended in err. nil means observe finalized it
// as succeeded; errRolledBack and errSettled mean observe (or the engine's own last resort) already owns the row
// and the containers that go with it.
func (e *Engine) finish(ctx context.Context, r *run, err error) {
	defer e.reportOutcome(ctx, r)
	switch {
	case err == nil, errors.Is(err, errRolledBack), errors.Is(err, errSettled):
		return // the row and the route already agree
	case errors.Is(err, errCancelled):
		e.cleanup(ctx, r.id, r.res.ID)
		e.terminal(ctx, r, StCancelled, "cancelled", "Cancelled", "deployment.cancelled")
	default:
		msg := err.Error()
		if r.logw != nil {
			r.logw.Sys("✖ " + msg)
		}
		e.cleanup(ctx, r.id, r.res.ID)
		e.terminal(ctx, r, StFailed, "failed", msg, "deployment.failed")
	}
}

// terminal writes a final status, audits it as the engine (system actor) and publishes events.
func (e *Engine) terminal(ctx context.Context, r *run, status, step, msg, action string) {
	if r.logw == nil {
		r.logw = newLogWriter(e.db, r.id, nil, 0, e.pub, e.cfg.Now)
	}
	var errv any
	if status != StSucceeded {
		errv = msg
	}
	err := store.InTx(ctx, e.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE deployments SET status=?, step=?, error=?, finished_at=?, observe_until=NULL WHERE id=? AND status NOT IN ('succeeded','failed','cancelled','interrupted','rolled_back')`,
			status, step, errv, ts(e.cfg.Now()), r.id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		meta := map[string]any{"resource_id": r.dep.ResourceID, "status": status}
		if status != StSucceeded {
			meta["error"] = msg
		}
		_, err = audit.Append(ctx, tx, audit.Entry{ActorType: "system", ActorID: "deploy-engine", Action: action, TargetType: "deployment", TargetID: r.id, Meta: meta})
		return err
	})
	if err != nil {
		slog.Warn("deploy: could not write terminal state", "deployment", r.id, "err", err)
	}
	e.emit(r.id)
}

// cleanup removes every container of this deployment that is not the resource's active one, found by label
// alone. It is idempotent, safe to run twice, and is what cancel, failure and boot recovery all call.
func (e *Engine) cleanup(ctx context.Context, depID, resourceID string) {
	cs, err := e.rt.ListContainers(ctx, runtime.LabelDeployment+"="+depID)
	if err != nil {
		slog.Warn("deploy: cleanup could not list containers", "deployment", depID, "err", err)
		return
	}
	var active string
	if resourceID != "" {
		_ = e.db.QueryRowContext(ctx, `SELECT COALESCE(active_deployment_id,'') FROM resources WHERE id=?`, resourceID).Scan(&active)
	}
	if active == depID {
		return // this deployment is serving: never remove the active container
	}
	for _, c := range cs {
		_ = e.rt.KillContainer(ctx, c.ID)
		if err := e.rt.RemoveContainer(ctx, c.ID); err != nil {
			slog.Warn("deploy: cleanup could not remove container", "container", c.ID, "err", err)
		}
	}
}

func (r *run) verifyTimeout() time.Duration {
	hc := r.res.Cfg.Healthcheck
	return time.Duration(hc.StartPeriodS+hc.Retries*(hc.IntervalS+hc.TimeoutS)+30) * time.Second
}
