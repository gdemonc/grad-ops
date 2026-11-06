// Command gradops runs the Grad-Ops control plane.
//
// Grad-Ops targets Linux only. This binary serves the REST API, owns the
// database, runs the durable worker and is the only process that can execute an
// approved action.
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
	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/config"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
	"github.com/gdemonc/grad-ops/internal/verify"
	"github.com/gdemonc/grad-ops/internal/worker"
)

// version is set at build time with
// -ldflags "-X main.version=<version>".
var version = "dev"

// defaultProbes are the read-only commands the stub planner runs. They answer
// "is this host healthy" without touching anything.
var defaultProbes = []string{
	"uname -a",
	"uptime",
	"df -h",
}

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

	engine, err := policy.New(policy.Config{
		Version:      cfg.PolicyVersion,
		AllowedRoots: cfg.AllowedRoots,
	})
	if err != nil {
		return fmt.Errorf("build policy: %w", err)
	}

	baseEnv := map[string]string{
		"PATH": os.Getenv("PATH"),
		"HOME": cfg.WorkDir,
	}
	mapWorkDir := func(string) string { return cfg.WorkDir }

	evidenceStore := evidence.New()

	approvals, err := approval.New(db, cfg.ApprovalTTL)
	if err != nil {
		return fmt.Errorf("build approvals: %w", err)
	}

	exec, err := executor.New(db, engine, approvals, evidenceStore, executor.Config{
		Bash:    cfg.Bash,
		BaseEnv: baseEnv,
	}, executor.WithWorkDirMapper(mapWorkDir))
	if err != nil {
		return fmt.Errorf("build executor: %w", err)
	}

	verifier, err := verify.New(db, evidenceStore, verify.Config{
		Bash:    cfg.Bash,
		BaseEnv: baseEnv,
		Timeout: cfg.ProbeTimeout,
	}, verify.WithWorkDirMapper(mapWorkDir))
	if err != nil {
		return fmt.Errorf("build verifier: %w", err)
	}

	investigator, err := investigation.New(db, engine,
		investigation.StubPlanner{Probes: defaultProbes},
		investigation.Config{
			Bash:            cfg.Bash,
			BaseEnv:         baseEnv,
			WorkDir:         cfg.WorkDir,
			ContractWorkDir: cfg.ContractWorkDir,
			Timeout:         cfg.ProbeTimeout,
			MaxOutputBytes:  cfg.MaxOutputBytes,
			MaxCalls:        len(defaultProbes),
		})
	if err != nil {
		return fmt.Errorf("build investigation: %w", err)
	}

	// The worker loop is cancelled by the same context as the HTTP server, so a
	// shutdown stops claiming new jobs and lets an in-flight one finish.
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()

	if cfg.EnableWorker {
		go func() {
			if err := wk.Run(workerCtx, investigator); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("worker stopped", "err", err)
			}
		}()
		logger.Info("worker loop started", "owner", workerID)
	} else {
		logger.Warn("worker loop disabled; tasks will queue without being investigated")
	}

	tasks := task.NewService(db)
	handler := api.New(api.Deps{
		Logger:          logger,
		Tasks:           tasks,
		DB:              db,
		Evidence:        evidenceStore,
		Approvals:       approvals,
		Executor:        exec,
		Verifier:        verifier,
		Version:         version,
		MaxRequestBytes: cfg.MaxRequestBytes,
		Runtime: api.RuntimeInfo{
			Bash:            cfg.Bash,
			WorkDir:         cfg.WorkDir,
			ContractWorkDir: cfg.ContractWorkDir,
			AllowedRoots:    cfg.AllowedRoots,
			PolicyVersion:   cfg.PolicyVersion,
			ApprovalTTLMS:   cfg.ApprovalTTL.Milliseconds(),
			WorkerEnabled:   cfg.EnableWorker,
		},
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
		"policy", cfg.PolicyVersion,
		"roots", cfg.AllowedRoots,
	)

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	stopWorker()

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
