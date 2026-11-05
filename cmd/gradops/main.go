// Command gradops runs the Grad-Ops control plane.
//
// Grad-Ops targets Linux only. This binary serves the REST API and owns the
// database; the durable worker and the execution path are added in later
// phases, and until then the service can create, read and audit tasks but it
// cannot perform host operations.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gdemonc/grad-ops/internal/api"
	"github.com/gdemonc/grad-ops/internal/config"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
	"github.com/gdemonc/grad-ops/internal/worker"
)

// version is set at build time with
// -ldflags "-X main.version=<version>".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gradops: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	// SIGINT and SIGTERM both mean "stop accepting work and drain".
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory %q: %w", cfg.DataDir, err)
	}

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return err
	}

	// Recover leases left behind by a previous process before serving traffic.
	// An expired lease means a worker died; it does not mean the work finished.
	// Read-only jobs return to the queue, anything else is abandoned as unknown
	// instead of being replayed on a guess.
	workerID := fmt.Sprintf("%s-%d", hostname(), os.Getpid())
	wk, err := worker.New(db, worker.DefaultConfig(workerID), logger)
	if err != nil {
		return err
	}
	recovered, err := wk.Recover(ctx)
	if err != nil {
		return fmt.Errorf("recover expired job leases: %w", err)
	}
	if recovered.Requeued > 0 || recovered.Abandoned > 0 {
		logger.Warn("recovered expired job leases",
			"requeued", recovered.Requeued,
			"abandoned", recovered.Abandoned,
		)
	}

	tasks := task.NewService(db)
	handler := api.New(api.Deps{
		Logger:          logger,
		Tasks:           tasks,
		DB:              db,
		Version:         version,
		MaxRequestBytes: cfg.MaxRequestBytes,
	})

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	logger.Info("gradops listening",
		"addr", cfg.Addr,
		"database", cfg.DBPath,
		"version", version,
	)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	logger.Info("gradops stopped")
	return nil
}

// hostname identifies this process in lease records. It falls back to a fixed
// name so a missing hostname never blocks startup.
func hostname() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "gradops"
	}
	return name
}
