// codeplod: the control-plane daemon (HTTP on loopback + unix socket, job worker).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/codeplo/codeplo/internal/api"
	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/config"
	"github.com/codeplo/codeplo/internal/modules/deploy"
	"github.com/codeplo/codeplo/internal/modules/deploy/builders"
	"github.com/codeplo/codeplo/internal/modules/edge"
	"github.com/codeplo/codeplo/internal/modules/extensions"
	"github.com/codeplo/codeplo/internal/modules/git"
	"github.com/codeplo/codeplo/internal/modules/git/providers"
	"github.com/codeplo/codeplo/internal/modules/previews"
	"github.com/codeplo/codeplo/internal/modules/runtime"
	"github.com/codeplo/codeplo/internal/modules/templates"
	"github.com/codeplo/codeplo/internal/modules/terminal"
	"github.com/codeplo/codeplo/internal/platform"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, key, err := app.Open(cfg)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := api.New(db, key, app.Version, app.Commit, app.BuiltAt)

	// Every module's service is built at boot; the flag in the database is checked per request (API) and per pass (loops).
	envOr := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	// Managed Caddy edge. The reconciler is always constructed, so turning the module on in the panel works at once
	// without a daemon restart; whether it acts is decided by the module flag on every pass. reconcileEdge is the
	// engine's route-refresh hook and is a no-op while the module is off.
	adminSocket := envOr("CODEPLO_CADDY_ADMIN_SOCKET", "/run/codeplo/caddy-admin.sock")
	rec := &edge.Reconciler{
		DB:      db,
		Client:  edge.NewClient(adminSocket),
		Opts:    edge.Options{AdminSocket: adminSocket, PanelSocket: cfg.SocketPath, ACMEEmail: os.Getenv("CODEPLO_ACME_EMAIL")},
		DataDir: envOr("CODEPLO_CADDY_DATA_DIR", filepath.Join(cfg.DataDir, "caddy")),
		Unit:    envOr("CODEPLO_CADDY_UNIT", "codeplo-caddy"),
		Run:     platform.DefaultRunner,
	}
	srv.Edge = rec
	reconcileEdge := func(c context.Context) error {
		if !srv.Reg.Enabled(c, "edge") {
			return nil
		}
		_, err := rec.Reconcile(c)
		return err
	}
	if srv.Reg.Enabled(ctx, "edge") {
		if _, err := rec.Reconcile(ctx); err != nil {
			slog.Warn("initial edge reconcile deferred to supervisor", "err", err)
		}
	}
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		failures := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !srv.Reg.Enabled(ctx, "edge") {
					continue
				}
				if err := reconcileEdge(ctx); err != nil {
					failures++
					if failures == 1 || failures%20 == 0 {
						slog.Warn("edge reconcile failed", "err", err, "consecutive", failures)
					}
					continue
				}
				failures = 0
			}
		}
	}()

	// Container runtime: shared by the container shell (terminal module), resource logs and the deploy engine. It
	// only dials the Docker socket per request, so wiring it never fails when Docker is absent; handlers then
	// report the runtime as unavailable.
	dockerSocket := envOr("CODEPLO_DOCKER_SOCKET", "/var/run/docker.sock")
	docker := runtime.NewDocker(dockerSocket)
	srv.Runtime = docker

	// Terminal services are built whatever the flag says; the route gate is what turns the module on and off.
	srv.Terminals = terminal.NewStore(db)
	srv.Tickets = terminal.NewTickets(terminal.TicketTTL)
	srv.HostShells = terminal.HelperSpawner{Socket: envOr("CODEPLO_PTY_SOCKET", "/run/codeplo-pty/pty.sock")}
	if n, err := srv.Terminals.Recover(ctx); err != nil {
		slog.Warn("terminal session recovery failed", "err", err)
	} else if n > 0 {
		slog.Info("closed terminal sessions left open by a previous run", "count", n)
	}

	// Git sources: connections, deploy keys, webhook secrets and hook tokens. Everything here is database and
	// master-key work, so it wires without a container runtime -- an operator can configure a source, and the key
	// it needs, before Docker is reachable.
	srv.Git = git.New(db, key)

	// The GitHub App is a daemon-level credential like the S3 destination rather than a database row: the private
	// key is the file GitHub handed out when the App was created, and a route that could write one after boot would
	// be a way in for anyone with panel:admin. Two things read it -- the engine's clone step, and the webhook
	// receiver, which has to be able to check a delivery the App signed for a repository the panel was never told
	// about. A key that will not parse stops the boot: an App nothing can authenticate as is worse than no App,
	// because every private source then fails inside a build with a git error that names none of this.
	ghCfg, ghSet, err := config.LoadGitHubApp()
	if err != nil {
		return err
	}
	// ghToken is the engine's port: nil when there is no App, in which case a clone uses only what its resource
	// carries, exactly as it did before the App existed.
	var ghToken func(context.Context, string) (string, bool, error)
	var reportStatus func(context.Context, providers.CommitStatus) error
	if ghSet {
		ghApp, err := providers.NewGitHubApp(ghCfg.AppID, ghCfg.InstallationID, ghCfg.KeyPEM, ghCfg.APIURL)
		if err != nil {
			return err
		}
		ghApp.WebhookSecret = ghCfg.WebhookSecret
		srv.GitHubApp = ghApp
		ghToken = ghApp.TokenFor
		reportStatus = ghApp.PostStatus
		slog.Info("github app configured", "app_id", ghApp.AppID, "installation_id", ghApp.InstallationID,
			"api", ghApp.APIURL, "clone_host", ghApp.CloneHost(), "webhook_secret", len(ghApp.WebhookSecret) > 0)
	}

	// Template catalog: the embedded bundle plus the operator's drop-in directory. The bundled files are read from
	// the binary, so wiring it cannot fail; a file that does not parse is logged here, which is where an operator
	// who has just dropped one in is looking before they try the panel.
	srv.Templates = &templates.Service{Catalog: templates.Catalog{Dir: cfg.TemplatesDir}, DB: db, Key: key}
	_, tplIssues := srv.Templates.List()
	for _, is := range tplIssues {
		slog.Warn("template file is not in the catalog", "file", is.File, "reason", is.Reason)
	}

	// Operations services: channels, metrics, backups and resource cron. Built before the deploy engine because the
	// engine's pre-deploy backup is one of them, and before the job queue starts because a backup attempt is a job
	// and a crashed daemon's open attempts have to be closed before a worker could pick one up.
	ops, err := wireOps(ctx, srv, cfg, db, key, docker)
	if err != nil {
		return err
	}

	// Deployment engine. Recovery runs before the queue starts: a deployment the previous process was mid-way
	// through has to become `interrupted`, and its leftover containers removed, before a worker could pick up
	// work that assumes a clean host. If Docker is down this cannot complete, and the daemon still boots -- the
	// queue simply reports the runtime as unavailable per deployment instead of the whole panel failing.
	var logSweep func(context.Context) error
	if docker != nil {
		eng := deploy.New(deploy.Deps{
			DB: db, Key: key, Runtime: docker,
			Builder: &builders.CLI{Socket: dockerSocket},
			Edge:    reconcileEdge,
			Publish: func(name string, data any) { srv.Bus.Publish(name, data) },
			// The pre-deploy snapshot is the databases module's, and nil when that module is off -- the engine's
			// backup step then logs that no snapshot was possible and continues.
			PreDeploy: ops.PreDeploy,
			// The App's installation token is the clone credential for a GitHub repository that carries none of its
			// own; nil when the daemon was started without one.
			GitHubToken: ghToken,
			// The same App tells the repository how the deploy went. Nil without an App, and every deployment
			// then ends with the commit as unmarked as it was before this existed.
			ReportStatus: reportStatus,
			DataDir:      cfg.DataDir,
			Config: deploy.Config{
				WorkDir:       filepath.Join(cfg.DataDir, "work"),
				VolumesRoot:   envOr("CODEPLO_VOLUMES_DIR", filepath.Join(cfg.DataDir, "volumes")),
				DockerDataDir: envOr("CODEPLO_DOCKER_DATA_DIR", "/var/lib/docker"),
				AppArmor:      platform.AppArmorEnabled(),
			},
		})
		srv.Deploy = eng
		logSweep = func(c context.Context) error {
			if _, err := eng.PruneLogs(c); err != nil {
				return err
			}
			// T37: same retention window as deployment logs for webhook delivery history.
			if srv.Git != nil {
				if n, err := srv.Git.PruneDeliveries(c); err != nil {
					return err
				} else if n > 0 {
					slog.Info("pruned webhook deliveries", "count", n)
				}
			}
			return nil
		}
		eng.Register(srv.Jobs) // handlers are a map the queue reads while running: register, then start
		if err := eng.Recover(ctx); err != nil {
			slog.Warn("deployment recovery incomplete; routes and containers may need reconciling", "err", err)
		}
	}

	// Pull-request previews. The service needs the engine it builds with and the queue a teardown's reap is written
	// to, so a panel without the deploy module has none — and the sweep starts here rather than in the receiver,
	// because the pass that takes expired and orphaned previews down is the module's promise, not a delivery's.
	// It runs with the feature flag off and with the base domain gone: an operator who turns previews off has asked
	// for their containers to stop running on this host.
	if srv.Deploy != nil {
		pv := previews.New(previews.Deps{
			DB: db, Engine: srv.Deploy, Queue: srv.Jobs, BaseDomain: config.PreviewBaseDomain(), Now: time.Now,
		})
		srv.Preview = pv
		// One pass before the loop sleeps: previews whose lifetime ended while this daemon was down are the reason
		// the sweep exists, and a branch nobody merged keeps holding the parent's credentials for the wait.
		go func() {
			if n, err := pv.Sweep(ctx); err != nil {
				slog.Warn("preview sweep at boot failed", "removed", n, "err", err)
			} else if n > 0 {
				slog.Info("preview sweep took previews down", "removed", n, "when", "boot")
			}
			everyMinute(ctx, "preview sweep", func(c context.Context) error {
				n, err := pv.Sweep(c)
				if n > 0 {
					slog.Info("preview sweep took previews down", "removed", n)
				}
				return err
			})
		}()
		st := pv.Check(ctx)
		// The domain and the flag are reported separately because they fail differently: one needs a DNS record and
		// the other needs a click. This is the one place the verdict is honest, because this is the process that has
		// the engine a preview is built with.
		slog.Info("pull request previews", "base_domain", st.BaseDomain, "domain_ready", st.DomainReady,
			"flag", st.FlagOn, "enabled", pv.Enabled(ctx), "live", st.Live, "ttl_hours", st.TTLHours,
			"max_per_service", st.MaxPerService, "memory_mb", st.MemoryMB)
		if st.DomainNote != "" {
			slog.Info("pull request previews are not naming anything", "reason", st.DomainNote)
		}
	}
	// Extensions outbound delivery (T55): only attempts when extensions.outbound is on.
	extSvc := extensions.New(db, key)
	go everyMinute(ctx, "extension delivery", func(c context.Context) error {
		n, err := extSvc.DeliverDue(c, 20)
		if n > 0 {
			slog.Info("extension deliveries sent", "count", n)
		}
		return err
	})
	go srv.Jobs.Run(ctx)
	ops.startLoops(ctx, srv, srv.Jobs, func(name string, data any) { srv.Bus.Publish(name, data) }, logSweep)

	// MCP (T59): a flag-gated, daemon-local unix socket (T53: no HTTP route). The listener and its connections are
	// tied to the daemon context inside startMCP; the defer removes the socket on clean shutdown.
	mcpPath, err := startMCP(ctx, cfg, db, srv)
	if err != nil {
		return err
	}
	defer os.Remove(mcpPath)

	if tok, issued, err := app.EnsureSetupToken(ctx, db); err != nil {
		return err
	} else if issued {
		// Shown once, to the service log only. Reissue with `codeplo setup-token`.
		fmt.Printf("\ncodeplo first-run setup (valid 1 hour, single use)\n  ssh -L 7443:127.0.0.1:7443 <this-host>\n  http://127.0.0.1:7443/setup?token=%s\n\n", tok)
	}

	loop, err := net.Listen("tcp", cfg.Listen) // mandatory break-glass listener
	if err != nil {
		return err
	}
	_ = os.Remove(cfg.SocketPath)
	sock, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return err
	}
	_ = os.Chmod(cfg.SocketPath, 0o660)

	mk := func(kind string) *http.Server {
		// no WriteTimeout: /events is a long-lived SSE stream
		return &http.Server{Handler: srv.Handler(kind), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	}
	hs := []*http.Server{mk("loopback"), mk("unix")}
	errc := make(chan error, 2)
	go func() { errc <- hs[0].Serve(loop) }()
	go func() { errc <- hs[1].Serve(sock) }()
	slog.Info("codeplod started", "listen", cfg.Listen, "socket", cfg.SocketPath, "version", app.Version)

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, h := range hs {
		_ = h.Shutdown(sctx)
	}
	return nil
}
