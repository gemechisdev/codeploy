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
	"syscall"
	"time"

	"github.com/codeplo/codeplo/internal/api"
	"github.com/codeplo/codeplo/internal/app"
	"github.com/codeplo/codeplo/internal/config"
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
