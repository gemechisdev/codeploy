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
	"github.com/codeplo/codeplo/internal/modules/runtime"
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

	// Managed Caddy edge: when the module is enabled the daemon owns the reconciler, runs one reconcile at
	// startup (non-fatal if Caddy is still coming up) and supervises it in a loop tied to the daemon context.
	envOr := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	// reconcileEdge is the engine's route-refresh hook; nil when the edge module is off, which the engine treats
	// as "nothing to do" rather than a failure.
	var reconcileEdge func(context.Context) error
	if srv.Reg.Enabled(ctx, "edge") {
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
		reconcileEdge = func(c context.Context) error { _, err := rec.Reconcile(c); return err }
		if _, err := rec.Reconcile(ctx); err != nil {
			slog.Warn("initial edge reconcile deferred to supervisor", "err", err)
		}
		go rec.Loop(ctx, 30*time.Second)
	}

	// Container runtime: shared by the container shell (terminal module), resource logs and the deploy engine. It
	// only dials the Docker socket per request, so wiring it never fails when Docker is absent; handlers then
	// report the runtime as unavailable.
	var docker *runtime.Docker
	dockerSocket := envOr("CODEPLO_DOCKER_SOCKET", "/var/run/docker.sock")
	if srv.Reg.Enabled(ctx, "terminal") || srv.Reg.Enabled(ctx, "deploy") {
		docker = runtime.NewDocker(dockerSocket)
		srv.Runtime = docker
	}
	if srv.Reg.Enabled(ctx, "terminal") {
		srv.Terminals = terminal.NewStore(db)
		srv.Tickets = terminal.NewTickets(terminal.TicketTTL)
		srv.HostShells = terminal.HelperSpawner{Socket: envOr("CODEPLO_PTY_SOCKET", "/run/codeplo-pty/pty.sock")}
		if n, err := srv.Terminals.Recover(ctx); err != nil {
			slog.Warn("terminal session recovery failed", "err", err)
		} else if n > 0 {
			slog.Info("closed terminal sessions left open by a previous run", "count", n)
		}
	}

	// Deployment engine. Recovery runs before the queue starts: a deployment the previous process was mid-way
	// through has to become `interrupted`, and its leftover containers removed, before a worker could pick up
	// work that assumes a clean host. If Docker is down this cannot complete, and the daemon still boots -- the
	// queue simply reports the runtime as unavailable per deployment instead of the whole panel failing.
	if srv.Reg.Enabled(ctx, "deploy") && docker != nil {
		eng := deploy.New(deploy.Deps{
			DB: db, Key: key, Runtime: docker,
			Builder: &builders.CLI{Socket: dockerSocket},
			Edge:    reconcileEdge,
			Publish: func(name string, data any) { srv.Bus.Publish(name, data) },
			DataDir: cfg.DataDir,
			Config: deploy.Config{
				WorkDir:       filepath.Join(cfg.DataDir, "work"),
				VolumesRoot:   envOr("CODEPLO_VOLUMES_DIR", filepath.Join(cfg.DataDir, "volumes")),
				DockerDataDir: envOr("CODEPLO_DOCKER_DATA_DIR", "/var/lib/docker"),
				AppArmor:      platform.AppArmorEnabled(),
			},
		})
		srv.Deploy = eng
		eng.Register(srv.Jobs) // handlers are a map the queue reads while running: register, then start
		if err := eng.Recover(ctx); err != nil {
			slog.Warn("deployment recovery incomplete; routes and containers may need reconciling", "err", err)
		}
	}
	go srv.Jobs.Run(ctx)

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
