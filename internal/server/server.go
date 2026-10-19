// Package server wires the app together: configuration, databases, storage, the HTTP API and the
// preview proxy, on one port.
package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/phinnerhq/sloppa/internal/accounts"
	"github.com/phinnerhq/sloppa/internal/artifacts"
	"github.com/phinnerhq/sloppa/internal/config"
	"github.com/phinnerhq/sloppa/internal/db"
	"github.com/phinnerhq/sloppa/internal/httpapi"
	"github.com/phinnerhq/sloppa/internal/preview"
	"github.com/phinnerhq/sloppa/internal/webui"
)

// shutdownGrace is how long requests in flight get to finish once asked to stop.
const shutdownGrace = 10 * time.Second

// Run serves until ctx is cancelled, then drains.
func Run(ctx context.Context, cfg *config.Config) error {
	state, err := db.OpenState(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer state.Close()

	accts, err := accounts.New(ctx, state, cfg.BootstrapUsername, cfg.BootstrapPassword)
	if err != nil {
		return err
	}

	store, err := artifacts.NewStore(ctx, cfg)
	if err != nil {
		return err
	}
	// The disk store holds its dir open; S3 holds nothing.
	if c, ok := store.(io.Closer); ok {
		defer c.Close()
	}
	arts, err := artifacts.Open(ctx, artifacts.Options{DataDir: cfg.DataDir, PublicURL: cfg.PublicURL, Store: store})
	if err != nil {
		return err
	}
	defer arts.Close()

	previews, err := preview.New(ctx, preview.Options{DB: state, Config: cfg.Preview, PublicURL: cfg.PublicURL, TrustProxy: cfg.TrustProxy})
	if err != nil {
		return err
	}

	static, err := staticFiles(cfg)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Handler:           httpapi.New(httpapi.Deps{Config: cfg, Accounts: accts, Artifacts: arts, Previews: previews, Static: static}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No read or write timeout: uploads, downloads and tunnels may all take long.
		BaseContext: func(net.Listener) context.Context { return context.WithoutCancel(ctx) },
	}
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	slog.Info("Listening on " + cfg.PublicURL)
	if cfg.Preview != nil {
		slog.Info("Serving previews as subdomains of " + cfg.Preview.Origin.String())
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	select {
	case err := <-served:
		previews.Close()
		return err
	case <-ctx.Done():
	}

	slog.Info("Shutting down")
	// Tunnels are hijacked connections, which Shutdown doesn't wait for: close them first, with a
	// code that tells the CLIs to reconnect once the server is back.
	previews.Close()
	drain, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(drain); err != nil {
		srv.Close()
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// staticFiles is STATIC_DIR when set, the dashboard built into the binary otherwise.
func staticFiles(cfg *config.Config) (fs.FS, error) {
	if cfg.StaticDir == "" {
		if built := webui.FS(); built != nil {
			return built, nil
		}
		slog.Warn("This binary was built without the dashboard: run pnpm build first, or set STATIC_DIR")
		return nil, nil
	}
	if _, err := os.Stat(filepath.Join(cfg.StaticDir, "index.html")); err != nil {
		return nil, fmt.Errorf("STATIC_DIR: %w", err)
	}
	return os.DirFS(cfg.StaticDir), nil
}
