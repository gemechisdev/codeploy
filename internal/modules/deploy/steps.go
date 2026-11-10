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
	"strconv"
	"strings"
	"time"

	"github.com/codeplo/codeplo/internal/modules/deploy/builders"
	"github.com/codeplo/codeplo/internal/modules/git"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/policy"
	"github.com/codeplo/codeplo/internal/store"
)

func (e *Engine) settingInt(ctx context.Context, key string, def int) int {
	var v string
	if err := e.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v); err != nil {
		return def
	}
	n, err := strconv.Atoi(strings.Trim(v, `" `))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func (e *Engine) settingBool(ctx context.Context, key string) bool {
	var v string
	_ = e.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	return strings.Trim(v, `" `) == "true"
}

// policyOptions are the per-resource facts the isolation policy judges against. None comes from user-supplied files.
func (r *run) policyOptions() policy.Options {
	cfg := r.res.Cfg
	return policy.Options{
		ResourceID: r.res.Slug, VolumesRoot: r.e.cfg.VolumesRoot, DockerDataDir: r.e.cfg.DockerDataDir,
		Trusted: cfg.Isolation.Trusted, AllowCaps: cfg.Isolation.CapAdd, AllowPublish: len(cfg.PublishedPorts) > 0,
		AppArmor: r.e.cfg.AppArmor,
	}
}

// specFor builds the requested (pre-policy) container for this deployment. role is runtime.RoleApp or RoleRelease.
func (r *run) specFor(role string) policy.Container {
	cfg := r.res.Cfg
	env := []string{}
	hasPort := false
	for _, v := range r.env {
		if v.Runtime() {
			env = append(env, v.Key+"="+v.Value)
			if v.Key == "PORT" {
				hasPort = true
			}
		}
	}
	if !hasPort {
		env = append(env, "PORT="+strconv.Itoa(cfg.Port))
	}
	c := policy.Container{
		Service: "app", Name: r.name, Image: r.tag, Env: env, Restart: cfg.Restart,
		Aliases: []string{r.res.Slug}, CapAdd: cfg.Isolation.CapAdd, Network: r.network,
		MemoryBytes: int64(cfg.Limits.MemoryMB) << 20, NanoCPUs: int64(cfg.Limits.CPUs * 1e9), PidsLimit: int64(cfg.Limits.PidsLimit),
		Labels: map[string]string{
			runtime.LabelResource: r.res.ID, runtime.LabelDeployment: r.id, runtime.LabelRole: role,
		},
	}
	if cfg.Isolation.User != nil && strings.TrimSpace(*cfg.Isolation.User) != "" {
		c.User = strings.TrimSpace(*cfg.Isolation.User)
	} else if r.res.Type == "database" {
		// Official redis/postgres/mysql images drop root→999 via setpriv/gosu. Our policy always sets
		// no-new-privileges:true, so that drop fails (setresuid EPERM) and the process never listens.
		// Starting as 999 skips the drop; volume dirs are 0777 so the data path stays writable.
		c.User = "999"
	}
	for _, v := range r.vols {
		c.Mounts = append(c.Mounts, policy.Mount{Type: "bind", Source: path.Join(r.e.cfg.VolumesRoot, r.res.Slug, v.Name), Target: v.MountPath})
	}
	if role == runtime.RoleApp {
		for _, p := range cfg.PublishedPorts {
			c.Ports = append(c.Ports, "127.0.0.1:"+strconv.Itoa(p)+":"+strconv.Itoa(p))
		}
	}
	return c
}

// ensureVolumeDirs creates each bind volume's directory under the resource's own tree.
func (r *run) ensureVolumeDirs() error {
	for _, v := range r.vols {
		if err := r.e.EnsureVolumeDir(r.res.Slug, v.Name); err != nil {
			return err
		}
	}
	return nil
}

func (r *run) prepare(ctx context.Context, _ context.CancelCauseFunc) error {
	e := r.e
	switch r.res.Type {
	case "app", "database", "static", "compose":
		// static: containerized static/nginx (or auto-detected static pack).
		// compose: docker compose project; still uses clone + compose up in acquire.
	default:
		return fail("preflight", "%s is not a supported resource type", r.res.Type)
	}
	// A database is the second type this engine runs, and it is the same container work with three differences: it
	// is observed by connecting to its port rather than asking a path (Postgres, MySQL and Redis answer their own
	// protocols), it takes the stop_first rollout because two servers must never share one data directory, and it
	// has no domain, so nothing routes to it and only the panel network can reach it.
	env, err := LoadEnv(ctx, e.db, e.key, r.res.ID)
	if err != nil {
		return fail("preflight", "%v", err)
	}
	r.env = env
	var secrets []string
	for _, v := range env {
		if v.Secret {
			secrets = append(secrets, v.Value)
		}
	}
	r.red = NewRedactor(secrets)
	r.logw = newLogWriter(e.db, r.id, r.red, e.settingInt(ctx, "deploy.max_log_lines", defaultMaxLog), e.pub, e.cfg.Now)
	r.logw.Sys(fmt.Sprintf("deployment %s of %s (%s, trigger %s)", r.id, r.res.Slug, r.dep.Mode, r.dep.Trigger))
	if err := e.rt.Available(ctx); err != nil {
		return fail("preflight", "Docker is not available on this host (%v). Install Docker Engine and ensure the codeplo user can use the socket, then redeploy.", err)
	}
	if r.vols, err = loadVolumes(ctx, e.db, r.res.ID); err != nil {
		return err
	}

	build := r.dep.Mode == "build" && !r.dep.ReusesImage()
	r.pf = e.preflight(build, e.settingInt(ctx, "disk_preflight_threshold_gb", 2))
	pj, _ := json.Marshal(map[string]any{"ok": r.pf.OK, "detail": r.pf.Detail, "disk_free_gb": r.pf.DiskFreeGB, "disk_required_gb": r.pf.DiskRequiredGB, "ram_ok": r.pf.RAMOK})
	_, _ = e.db.ExecContext(ctx, `UPDATE deployments SET preflight_json=? WHERE id=?`, string(pj), r.id)
	if !r.pf.OK {
		return fail("preflight", "%s", r.pf.Detail)
	}
	r.buildMem = r.pf.BuildMemory

	r.network = lower("cpl-env-" + r.res.EnvID)
	r.tag = "codeplo/" + r.res.Slug + ":" + r.id
	r.name = "cpl-" + r.res.Slug + "-" + r.id

	// Policy gate: validated before anything is pulled, built or created. The same container is validated
	// again, byte for byte, at creation time (policy.Approved is the only thing the runtime accepts).
	r.spec = r.specFor(runtime.RoleApp)
	pr := policy.Enforce(r.spec, r.policyOptions())
	for _, w := range pr.Warnings {
		r.logw.Sys("policy warning: " + w.String())
	}
	if !pr.OK() {
		return fail("preflight", "Isolation policy rejected this deployment: %s", strings.Join(pr.Messages(), "; "))
	}
	r.flags = pr.Flags
	for _, f := range r.flags {
		r.logw.Sys("policy opt-in in effect: " + f)
	}
	if r.res.Active != "" {
		if p, err := loadDep(ctx, e.db, r.res.Active); err == nil {
			r.prev = p
		}
	}
	return nil
}

// watchDisk cancels the step when free space drops under the safety floor. Returns the stop function.
func (r *run) watchDisk(ctx context.Context, cancel context.CancelCauseFunc) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(r.e.cfg.DiskWatchEvery)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				free, total, err := r.e.cfg.Disk(r.e.cfg.WorkDir)
				floor := total / 50
				if floor < 512*mib {
					floor = 512 * mib
				}
				if err == nil && free < floor {
					cancel(errDiskLow)
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func pickDigest(info runtime.ImageInfo) string {
	for _, d := range info.RepoDigests {
		if i := strings.Index(d, "@"); i >= 0 {
			return d[i+1:]
		}
	}
	return info.ID
}

func (r *run) logFn() builders.LogFunc {
	return func(stream, line string) { r.logw.Line(stream, line) }
}

type sourceRow struct {
	Kind, RepoURL, Branch, BuildPath, Dockerfile string
}

func loadSource(ctx context.Context, db *sql.DB, resID string) (sourceRow, bool) {
	var s sourceRow
	var url, br, bp, df sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT kind, repo_url, branch, build_path, dockerfile FROM resource_sources WHERE resource_id=?`, resID).Scan(&s.Kind, &url, &br, &bp, &df); err != nil {
		return s, false
	}
	s.RepoURL, s.Branch, s.BuildPath, s.Dockerfile = url.String, br.String, bp.String, df.String
	return s, true
}

// acquire produces the image: a retained one for rollback, a registry pull, or a clone + BuildKit build. The
// image is always tagged codeplo/<slug>:<deployment> so retention and rollback address it by name.
func (r *run) acquire(ctx context.Context, cancelStep context.CancelCauseFunc) error {
	e := r.e
	repo := "codeplo/" + r.res.Slug
	stopWatch := r.watchDisk(ctx, cancelStep)
	defer stopWatch()

	var digest string
	switch {
	case r.dep.ReusesImage():
		var targetDigest sql.NullString
		_ = e.db.QueryRowContext(ctx, `SELECT image_digest FROM deployments WHERE id=?`, r.dep.RollbackOf).Scan(&targetDigest)
		verb := "reusing retained image"
		if r.dep.Trigger == TrRollback {
			verb = "rolling back to retained image"
		}
		r.logw.Sys(verb + " " + r.dep.ImageRef + " (no rebuild)")
		if _, err := e.rt.ImageInspect(ctx, r.dep.ImageRef); err != nil {
			if errors.Is(err, runtime.ErrNotFound) {
				return fail("build", "The image of the deployment you are rolling back to is no longer retained (cleanup or the retain_images limit removed it). Deploy again instead.")
			}
			return err
		}
		if err := e.rt.TagImage(ctx, r.dep.ImageRef, repo, r.id); err != nil {
			return err
		}
		digest = targetDigest.String

	case r.dep.Mode == "pull":
		ref := r.dep.ImageRef
		if !ValidImageRef(ref) {
			return fail("build", "%q is not a valid image reference", ref)
		}
		req := builders.PullRequest{Ref: ref}
		if rc := registryFor(ctx, e.db, e.key, r.res.ID); rc != nil {
			req.Auth = &builders.RegistryAuth{Server: rc.Server, Username: rc.Username, Password: rc.Password}
			r.red = NewRedactor(append(secretsOf(r.env), rc.Password))
			r.logw.red = r.red
		}
		r.logw.Sys("pulling " + ref)
		if err := e.b.Pull(ctx, req, r.logFn()); err != nil {
			return err
		}
		info, err := e.rt.ImageInspect(ctx, ref)
		if err != nil {
			return err
		}
		digest = pickDigest(info)
		if err := e.rt.TagImage(ctx, ref, repo, r.id); err != nil {
			return err
		}

	default:
		var err error
		if digest, err = r.buildFromSource(ctx, repo); err != nil {
			return err
		}
	}
	r.logw.Sys("image ready: " + digest)
	_, err := e.db.ExecContext(ctx, `UPDATE deployments SET image_digest=? WHERE id=?`, digest, r.id)
	return err
}

func secretsOf(env []EnvVar) []string {
	var out []string
	for _, v := range env {
		if v.Secret {
			out = append(out, v.Value)
		}
	}
	return out
}

func (r *run) buildFromSource(ctx context.Context, repo string) (string, error) {
	e := r.e
	src, ok := loadSource(ctx, e.db, r.res.ID)
	if !ok || src.Kind == "registry" || src.RepoURL == "" {
		return "", fail("build", "Configure a Git source before building on the host.")
	}
	work := filepath.Join(e.cfg.WorkDir, r.id)
	_ = os.RemoveAll(work) // idempotent: a rerun starts from nothing
	if err := os.MkdirAll(work, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	dest := filepath.Join(work, "src")

	branch := r.dep.Branch
	if branch == "" {
		branch = src.Branch
	}
	// The git settings bound this clone: the same numbers the panel writes are the ones the engine enforces, so a
	// limit an operator raises actually reaches the step that needs it.
	limits := git.ReadCloneLimits(ctx, e.db)
	gs := builders.GitSource{URL: src.RepoURL, Branch: branch, MaxBytes: limits.MaxBytes}
	// by names, for the log line, which credential this clone is about to use: "cloning X (main)" tells an
	// operator nothing when the clone fails on authentication an hour from now.
	by := ""
	if src.Kind == "deploy_key_git" {
		k := credential(ctx, e.db, e.key, r.res.ID, "deploy_key")
		if k == nil {
			return "", fail("build", "This resource uses a deploy key but none is stored. Add one, then redeploy.")
		}
		gs.DeployKey = k
		by = " with its deploy key"
	} else if t := credential(ctx, e.db, e.key, r.res.ID, "git_token"); t != nil {
		gs.Token = string(t)
		r.red = NewRedactor(append(secretsOf(r.env), string(t)))
		r.logw.red = r.red
		by = " with its stored token"
	} else if e.ghToken != nil {
		// The App is the last credential this panel reaches for, and only for a repository on its forge that the
		// installation covers: a source the operator gave its own key or token never silently changes hands.
		tok, used, err := e.ghToken(ctx, src.RepoURL)
		if err != nil {
			return "", fail("build", "The GitHub App could not authorise this clone: %v", err)
		}
		if used {
			gs.Token = tok
			r.red = NewRedactor(append(secretsOf(r.env), tok))
			r.logw.red = r.red
			by = " through the GitHub App installation"
		}
	}
	r.logw.Sys("cloning " + src.RepoURL + " (" + branch + by + ")")
	// The clone gets its own deadline: a host that black-holes the connection has to fail this deployment after
	// git.clone_timeout_s, not hold the worker until the whole build budget is gone.
	cloneCtx, cancelClone := context.WithTimeout(ctx, limits.Timeout)
	cr, err := e.b.Clone(cloneCtx, gs, dest, r.logFn())
	timedOut := errors.Is(cloneCtx.Err(), context.DeadlineExceeded)
	cancelClone()
	if err != nil {
		if timedOut {
			return "", fail("build", "Cloning this repository took more than %s and was stopped. Raise git.clone_timeout_s, or build the image in CI and deploy it with a hook.", limits.Timeout)
		}
		return "", err
	}
	_, _ = e.db.ExecContext(ctx, `UPDATE deployments SET commit_sha=?, commit_message=? WHERE id=?`, cr.Commit, cr.Message, r.id)
	// The commit is now known, which is the earliest point a forge can be told anything: a status is keyed by sha,
	// so the pending marker a push deserves can only be written once the push's commit has been checked out.
	e.reportOutcome(ctx, r)

	// Compose stacks: docker compose up in the clone (Coolify-style). Single-service apps keep the image path below.
	if r.res.Type == "compose" {
		project := "cpl-" + r.res.Slug
		composeFile := deref(r.res.Cfg.Image.Dockerfile, "") // reuse dockerfile field for compose file name when set
		// Image.Dockerfile is normally a Dockerfile path; for compose resources operators may set compose.yaml here.
		if composeFile == "Dockerfile" || composeFile == "" {
			composeFile = ""
		}
		r.logw.Sys("docker compose up (project " + project + ")")
		if err := e.b.ComposeUp(ctx, project, dest, composeFile, r.logFn()); err != nil {
			return "", fail("build", "compose up failed: %v", err)
		}
		r.composeDone = true
		// Prefer the primary container as the deployment's identity for logs/resolve.
		if c, err := e.rt.Resolve(ctx, r.res.ID); err == nil {
			r.newCID = c.ID
		}
		return "compose:" + project, nil
	}

	ctxRel := path.Join(src.BuildPath, deref(r.res.Cfg.Image.Context, "."))
	ctxDir, err := builders.ConfinePath(dest, ctxRel)
	if err != nil {
		return "", fail("build", "Build context rejected: %v", err)
	}
	// Dockerfile if present; else auto-detect stack and optional build.* command overrides (Render/Koyeb-style).
	preferred := deref(r.res.Cfg.Image.Dockerfile, deref(&src.Dockerfile, "Dockerfile"))
	ov := builders.BuildOverrides{Dockerfile: preferred}
	if b := r.res.Cfg.Build; b != nil {
		ov.InstallCommand = deref(b.InstallCommand, "")
		ov.BuildCommand = deref(b.BuildCommand, "")
		ov.StartCommand = deref(b.StartCommand, "")
		ov.TestCommand = deref(b.TestCommand, "")
		ov.Runtime = deref(b.Runtime, "")
		// Empty dockerfile in config + non-empty start/install means prefer command plan over a stale "Dockerfile" name.
		if preferred == "Dockerfile" && (ov.InstallCommand != "" || ov.StartCommand != "" || ov.BuildCommand != "") {
			if _, err := os.Stat(filepath.Join(ctxDir, "Dockerfile")); err != nil {
				ov.Dockerfile = ""
			}
		}
	}
	pack, err := builders.EnsureDockerfileWithOverrides(ctxDir, ov, r.logFn())
	if err != nil {
		return "", fail("build", "%v", err)
	}
	r.logw.Sys(pack.Note)
	if pack.Port > 0 && r.res.Cfg.Port == 3000 {
		// Keep operator-chosen ports; only hint when still on default — do not mutate stored config mid-deploy.
	}
	dfRel := path.Join(ctxRel, pack.Dockerfile)
	df, err := builders.ConfinePath(dest, dfRel)
	if err != nil {
		return "", fail("build", "Dockerfile rejected: %v", err)
	}
	req := builders.BuildRequest{ContextDir: ctxDir, Dockerfile: df, Tag: r.tag, MemoryBytes: r.buildMem,
		Secrets: map[string]string{}, Args: map[string]string{}}
	for _, v := range r.env {
		if !v.Build() {
			continue
		}
		if v.Secret {
			req.Secrets[v.Key] = v.Value // BuildKit secret mount, never ARG
		} else {
			req.Args[v.Key] = v.Value
		}
	}
	if rc := registryFor(ctx, e.db, e.key, r.res.ID); rc != nil {
		req.Auth = &builders.RegistryAuth{Server: rc.Server, Username: rc.Username, Password: rc.Password}
	}
	r.logw.Sys("building image " + r.tag)
	if err := e.b.Build(ctx, req, r.logFn()); err != nil {
		return "", err
	}
	info, err := e.rt.ImageInspect(ctx, r.tag)
	if err != nil {
		return "", err
	}
	_ = repo
	return info.ID, nil
}

func deref(p *string, def string) string {
	if p == nil || *p == "" {
		return def
	}
	return *p
}

// backupTimeout bounds the pre-deploy snapshot. The dump carries its own bound inside the databases service
// (backup.timeout_s), so the step only has to be looser than that: a tighter step bound would kill a dump the
// module was still willing to finish, and the deployment would then fail blaming a timeout on a step that was not
// the slow part.
func (r *run) backupTimeout(ctx context.Context) time.Duration {
	if r.e.preDeploy == nil {
		return time.Minute // the step returns immediately; the bound only needs to exist
	}
	return time.Duration(r.e.settingInt(ctx, "backup.timeout_s", 900))*time.Second + time.Minute
}

// preDeployBackup takes the snapshot the pre-deploy rule asks for. It is a step of its own rather than a line inside
// release or start because its failure has to stop the deployment before either of those runs, and because the
// deployment log is the operator's proof of what happened to their database today.
func (r *run) preDeployBackup(ctx context.Context, _ context.CancelCauseFunc) error {
	if r.e.preDeploy == nil {
		r.logw.Sys("no database module on this daemon: skipping the pre-deploy backup")
		return nil
	}
	taken, err := r.e.preDeploy(ctx, r.res.ID)
	if err != nil {
		// The deployment stops here. Proceeding would replace a database the panel has just failed to copy, and a
		// failed migration on data that was never snapshotted is the one accident this step exists to prevent.
		return fail("backup", "%s The deployment was stopped before the new version touched your database.", err)
	}
	switch {
	case taken:
		r.logw.Sys("pre-deploy backup of the resource's database completed")
	default:
		// Not a warning: most resources are application containers with no schedule, and a deployment log that
		// called that out on every deploy would teach the operator to ignore it.
		if r.res.Type == "database" {
			r.logw.Sys("no backup schedule on this database: no pre-deploy snapshot taken")
		}
	}
	return nil
}

// release runs the optional release command in a one-off container before the switch. It uses the container
// lifecycle only (create, start, wait, kill, remove): this daemon cannot interrupt an exec, so a hung release
// command is ended by killing its container when the step's context ends.
func (r *run) release(ctx context.Context, _ context.CancelCauseFunc) error {
	e := r.e
	if err := r.ensureVolumeDirs(); err != nil {
		return err
	}
	if err := e.rt.EnsureNetwork(ctx, r.network); err != nil {
		return err
	}
	c := r.specFor(runtime.RoleRelease)
	c.Name = r.name + "-release"
	c.Cmd = r.res.Cfg.ReleaseCommand
	c.Restart = "no"
	c.Aliases = nil
	pr := policy.Enforce(c, r.policyOptions())
	if !pr.OK() {
		return fail("release", "Isolation policy rejected the release container: %s", strings.Join(pr.Messages(), "; "))
	}
	r.logw.Sys("running release command: " + strings.Join(r.res.Cfg.ReleaseCommand, " "))
	id, err := e.rt.CreateContainer(ctx, pr.Approved)
	if err != nil {
		return err
	}
	bg := context.WithoutCancel(ctx)
	defer func() {
		cctx, cancel := context.WithTimeout(bg, 30*time.Second)
		defer cancel()
		_ = e.rt.KillContainer(cctx, id)
		_ = e.rt.RemoveContainer(cctx, id)
	}()
	if err := e.rt.StartContainer(ctx, id); err != nil {
		return err
	}
	code, werr := e.rt.WaitContainer(ctx, id)
	lctx, lcancel := context.WithTimeout(bg, 10*time.Second)
	defer lcancel()
	_ = e.rt.Logs(lctx, id, runtime.LogOptions{Tail: 200}, func(l runtime.LogLine) error {
		r.logw.Line(l.Stream, l.Line)
		return nil
	})
	if werr != nil {
		return werr // timeout or cancellation: the deferred kill ends the container
	}
	if code != 0 {
		return fail("release", "The release command failed (exit code %d), so the deployment was stopped before the switch; the previous version is still serving. Database changes the command made are NOT rolled back (application rollback is not database rollback).", code)
	}
	r.logw.Sys("release command succeeded")
	return nil
}

// stopOldFirst is the resource's rollout choice with the daemon-wide default from settings underneath it: a host
// with no memory for two containers at once opts in globally instead of per resource.
func (r *run) stopOldFirst(ctx context.Context) bool {
	switch r.res.Cfg.Rollout {
	case "stop_first":
		return true
	case "start_first":
		return false
	default:
		return r.e.settingBool(ctx, "deploy.stop_old_first")
	}
}

// start creates and starts the new container. The previous one is left alone (blue/green) unless the resource
// opted into stop_first for very small hosts.
func (r *run) start(ctx context.Context, _ context.CancelCauseFunc) error {
	e := r.e
	if r.composeDone {
		// Stack is already running from compose up. Resolve a container and record upstream for the edge.
		if r.newCID == "" {
			if c, err := e.rt.Resolve(ctx, r.res.ID); err == nil {
				r.newCID = c.ID
			}
		}
		if r.newCID == "" {
			// Fall back: any container on the environment network is better than failing the deploy.
			r.logw.Sys("compose is up; no codeplo.resource label found — set container labels or publish a domain after inspecting docker compose ps")
			r.upstream = "127.0.0.1:" + strconv.Itoa(r.res.Cfg.Port)
			_, _ = e.db.ExecContext(ctx, `UPDATE deployments SET upstream=?, container_id=? WHERE id=?`, r.upstream, r.newCID, r.id)
			return nil
		}
		st, err := e.rt.InspectContainer(ctx, r.newCID, r.network)
		if err != nil {
			return err
		}
		port := r.res.Cfg.Port
		if port <= 0 {
			port = 80
		}
		r.upstream = st.IP + ":" + strconv.Itoa(port)
		r.logw.Sys("compose service at " + r.upstream)
		_, err = e.db.ExecContext(ctx, `UPDATE deployments SET upstream=?, container_id=? WHERE id=?`, r.upstream, r.newCID, r.id)
		return err
	}
	if err := r.ensureVolumeDirs(); err != nil {
		return err
	}
	if err := e.rt.EnsureNetwork(ctx, r.network); err != nil {
		return err
	}
	if r.stopOldFirst(ctx) && r.prev.ContainerID != "" {
		r.logw.Sys("stop-old-first: stopping the previous container (no safe-rollback window on this resource)")
		if err := e.rt.StopContainer(ctx, r.prev.ContainerID, e.cfg.StopGraceS); err != nil {
			return err
		}
	}
	pr := policy.Enforce(r.spec, r.policyOptions())
	if !pr.OK() {
		return fail("start", "Isolation policy rejected the container: %s", strings.Join(pr.Messages(), "; "))
	}
	id, err := e.rt.CreateContainer(ctx, pr.Approved)
	if err != nil {
		return err
	}
	r.newCID = id
	if _, err := e.db.ExecContext(ctx, `UPDATE deployments SET container_id=? WHERE id=?`, id, r.id); err != nil {
		return err
	}
	if err := e.rt.StartContainer(ctx, id); err != nil {
		return err
	}
	var st runtime.State
	for i := 0; i < 10; i++ {
		if st, err = e.rt.InspectContainer(ctx, id, r.network); err != nil {
			return err
		}
		if st.IP != "" || !st.Running {
			break
		}
		if err := e.cfg.Sleep(ctx, 300*time.Millisecond); err != nil {
			return err
		}
	}
	if st.IP == "" {
		return fail("start", "The container started but has no address on the environment network (it may have exited immediately).")
	}
	r.upstream = st.IP + ":" + strconv.Itoa(r.res.Cfg.Port)
	r.logw.Sys("container " + shortID(id) + " started at " + r.upstream)
	_, err = e.db.ExecContext(ctx, `UPDATE deployments SET upstream=? WHERE id=?`, r.upstream, r.id)
	return err
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (r *run) tailLogs() {
	bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if r.newCID == "" {
		return
	}
	r.logw.Sys("--- last container output ---")
	_ = r.e.rt.Logs(bg, r.newCID, runtime.LogOptions{Tail: 30}, func(l runtime.LogLine) error {
		r.logw.Line(l.Stream, l.Line)
		return nil
	})
}

// exitReason explains a container that is not running.
func (r *run) exitReason(st runtime.State) string {
	if st.OOMKilled {
		return fmt.Sprintf("it was killed for running out of memory (limit %d MB); raise the memory limit in the resource settings", r.res.Cfg.Limits.MemoryMB)
	}
	return fmt.Sprintf("it exited with code %d", st.ExitCode)
}

// verify waits for the new container to answer its health check. Nothing here touches traffic.
func (r *run) verify(ctx context.Context, _ context.CancelCauseFunc) error {
	e := r.e
	hc := r.res.Cfg.Healthcheck
	_, _ = e.db.ExecContext(ctx, `UPDATE deployments SET health_max=?, health_done=0 WHERE id=?`, hc.Retries, r.id)
	for i := 0; i < hc.StartPeriodS; i++ {
		st, err := e.rt.InspectContainer(ctx, r.newCID, r.network)
		if err != nil {
			return err
		}
		if !st.Running {
			r.tailLogs()
			return fail("health", "The new container stopped during its start period: %s. The previous version is still serving.", r.exitReason(st))
		}
		if err := e.cfg.Sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	var last string
	for attempt := 1; attempt <= hc.Retries; attempt++ {
		st, err := e.rt.InspectContainer(ctx, r.newCID, r.network)
		if err != nil {
			return err
		}
		if !st.Running {
			r.tailLogs()
			return fail("health", "The new container stopped before it became healthy: %s. The previous version is still serving.", r.exitReason(st))
		}
		status, perr := r.probe(ctx, time.Duration(hc.TimeoutS)*time.Second)
		if perr != nil {
			status, last = perr.Error(), perr.Error()
		}
		_, _ = e.db.ExecContext(ctx, `UPDATE deployments SET health_done=?, health_last=? WHERE id=?`, attempt, status, r.id)
		if perr == nil {
			r.logw.Sys(fmt.Sprintf("health check passed on attempt %d/%d", attempt, hc.Retries))
			return nil
		}
		r.logw.Sys(fmt.Sprintf("health check %d/%d failed: %s", attempt, hc.Retries, perr))
		if attempt < hc.Retries {
			if err := e.cfg.Sleep(ctx, time.Duration(hc.IntervalS)*time.Second); err != nil {
				return err
			}
		}
	}
	r.tailLogs()
	return fail("health", "The new version never became healthy after %d checks (last result: %s). The previous version is still serving.", hc.Retries, last)
}

// switchTraffic is the only place that moves traffic: one transaction sets resources.active_deployment_id, and
// the edge derives its upstream from that column alone.
func (r *run) switchTraffic(ctx context.Context, _ context.CancelCauseFunc) error {
	e := r.e
	if r.newCID == "" || r.upstream == "" {
		return fail("switch", "internal error: no verified container to switch to")
	}
	err := store.InTx(ctx, e.db, func(tx *sql.Tx) error {
		var prev sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT active_deployment_id FROM resources WHERE id=?`, r.res.ID).Scan(&prev); err != nil {
			return err
		}
		var cr int
		if err := tx.QueryRowContext(ctx, `SELECT cancel_requested FROM deployments WHERE id=?`, r.id).Scan(&cr); err != nil {
			return err
		}
		if cr == 1 {
			return errCancelled
		}
		if _, err := tx.ExecContext(ctx, `UPDATE deployments SET previous_deployment_id=?, retained=1 WHERE id=?`, nullable(prev.String), r.id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE resources SET active_deployment_id=?, desired_state='running', started_at=? WHERE id=?`, r.id, ts(e.cfg.Now()), r.res.ID)
		if err != nil {
			return err
		}
		// domains keeps a copy of the address it is serving: the edge can render a route from it if the derived
		// lookup is momentarily empty, which turns a transient DB state into a kept-alive route instead of a 404.
		_, err = tx.ExecContext(ctx, `UPDATE domains SET upstream=? WHERE resource_id=?`, r.upstream, r.res.ID)
		return err
	})
	if err != nil {
		return err
	}
	r.switched = true
	r.logw.Sys("traffic switched to " + r.id)
	e.syncEdge(ctx)
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// syncEdge asks the edge to re-render. Best effort: the supervisor loop repairs drift if Caddy is down.
func (e *Engine) syncEdge(ctx context.Context) {
	if e.edg == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if err := e.edg(cctx); err != nil {
		e.pub("edge.changed", map[string]any{"deferred": true})
		return
	}
	e.pub("edge.changed", map[string]any{})
}
