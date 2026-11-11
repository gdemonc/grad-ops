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
	"os/user"
	"strings"
	"syscall"
	"time"

	"github.com/gdemonc/grad-ops/internal/api"
	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/config"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/llm"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/shell"
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

	// Refuse to start with a boundary this host cannot enforce. Validating here
	// keeps the failure at startup instead of on the first investigation.
	logger.Info("validating shell runtime")
	if err := shell.ValidateRuntime(cfg.ExecutorUser, cfg.Limits, cfg.Prlimit); err != nil {
		return fmt.Errorf("runtime configuration: %w", err)
	}
	logger.Info("validating cgroup runtime", "mode", cfg.RuntimeMode)
	if err := shell.ValidateCgroup(cfg.Cgroup); err != nil {
		return fmt.Errorf("cgroup runtime: %w", err)
	}
	sandbox := shell.SandboxConfig{
		Enabled:       cfg.SandboxMode == "bubblewrap",
		Bubblewrap:    cfg.Bubblewrap,
		AllowNetwork:  cfg.SandboxNetwork,
		WritablePaths: cfg.AllowedRoots,
	}
	logger.Info("validating sandbox runtime", "mode", cfg.SandboxMode)
	if err := shell.ValidateSandbox(sandbox); err != nil {
		return fmt.Errorf("sandbox runtime: %w", err)
	}
	logger.Info("runtime boundary validated",
		"runtime", cfg.RuntimeMode,
		"sandbox", cfg.SandboxMode,
		"executor_user", cfg.ExecutorUser,
	)

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
		Bash:              cfg.Bash,
		BaseEnv:           baseEnv,
		RequireRunAsMatch: cfg.RequireRunAsMatch,
		RunAs:             cfg.ExecutorUser,
		Limits:            cfg.Limits,
		Cgroup:            cfg.Cgroup,
		Sandbox:           sandbox,
		ExecutionTimeout:  cfg.ExecutionTimeout,
	}, executor.WithWorkDirMapper(mapWorkDir))
	if err != nil {
		return fmt.Errorf("build executor: %w", err)
	}

	verifier, err := verify.New(db, evidenceStore, verify.Config{
		Bash:    cfg.Bash,
		BaseEnv: baseEnv,
		Timeout: cfg.ProbeTimeout,
		RunAs:   cfg.ExecutorUser,
		Limits:  cfg.Limits,
		Cgroup:  cfg.Cgroup,
		Sandbox: sandbox,
	}, verify.WithWorkDirMapper(mapWorkDir))
	if err != nil {
		return fmt.Errorf("build verifier: %w", err)
	}

	planner, err := buildPlanner(cfg, logger)
	if err != nil {
		return err
	}

	investigator, err := investigation.New(db, engine,
		planner,
		investigation.Config{
			Bash:               cfg.Bash,
			BaseEnv:            baseEnv,
			WorkDir:            cfg.WorkDir,
			ContractWorkDir:    cfg.ContractWorkDir,
			Timeout:            cfg.ProbeTimeout,
			MaxOutputBytes:     cfg.MaxOutputBytes,
			MaxCalls:           plannerMaxCalls(cfg),
			PersistentProbes:   cfg.PersistentProbes,
			ScratchDir:         cfg.DataDir,
			TotalTimeout:       cfg.InvestigationTimeout,
			AgentLoop:          cfg.AgentLoop,
			AgentRounds:        cfg.AgentRounds,
			AgentCallsPerRound: cfg.AgentCallsPerRound,
			AgentContextBytes:  cfg.AgentContextBytes,
			RunAs:              cfg.ExecutorUser,
			Limits:             cfg.Limits,
			Cgroup:             cfg.Cgroup,
			Sandbox:            sandbox,
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
		APIToken:        cfg.APIToken,
		Runtime: api.RuntimeInfo{
			Bash:                  cfg.Bash,
			WorkDir:               cfg.WorkDir,
			ContractWorkDir:       cfg.ContractWorkDir,
			AllowedRoots:          cfg.AllowedRoots,
			PolicyVersion:         cfg.PolicyVersion,
			ApprovalTTLMS:         cfg.ApprovalTTL.Milliseconds(),
			WorkerEnabled:         cfg.EnableWorker,
			RequireRunAsMatch:     cfg.RequireRunAsMatch,
			PersistentProbes:      cfg.PersistentProbes,
			ExecutorUser:          cfg.ExecutorUser,
			CPULimitMS:            cfg.Limits.CPU.Milliseconds(),
			AddressSpaceBytes:     cfg.Limits.AddressSpaceBytes,
			ProcessLimit:          cfg.Limits.MaxProcesses,
			FileSizeBytes:         cfg.Limits.MaxFileSizeBytes,
			OpenFileLimit:         cfg.Limits.MaxOpenFiles,
			Planner:               cfg.Planner,
			AgentLoop:             cfg.AgentLoop,
			RuntimeMode:           cfg.RuntimeMode,
			CgroupMemoryMaxBytes:  cfg.Cgroup.MemoryMaxBytes,
			CgroupPidsMax:         cfg.Cgroup.PidsMax,
			CgroupCPUQuotaMicros:  cfg.Cgroup.CPUQuotaMicros,
			CgroupCPUPeriodMicros: cfg.Cgroup.CPUPeriodMicros,
			SandboxMode:           cfg.SandboxMode,
			APITokenSet:           cfg.APIToken != "",
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

// buildPlanner chooses the deterministic stub or the model-backed planner.
//
// The model is untrusted input to the control plane. Building it here does not
// grant it anything: probes still pass the read-only analyzer, actions still
// pass policy, and a human still approves what policy requires.
func buildPlanner(cfg config.Config, logger *slog.Logger) (investigation.Planner, error) {
	if cfg.Planner != "model" {
		return investigation.StubPlanner{Probes: defaultProbes}, nil
	}

	client, err := llm.NewOpenAI(llm.Config{
		BaseURL:          cfg.LLMBaseURL,
		APIKey:           cfg.LLMAPIKey,
		Model:            cfg.LLMModel,
		Timeout:          cfg.LLMTimeout,
		MaxResponseBytes: cfg.LLMMaxResponseBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("build LLM client: %w", err)
	}

	// A model does not know the host's service account. Fill run_as with the
	// configured executor identity, or with the current non-root user, so an
	// action is not rejected merely for omitting a field the model cannot know.
	runAs := strings.TrimSpace(cfg.ExecutorUser)
	if runAs == "" {
		if current, err := user.Current(); err == nil {
			runAs = current.Username
		}
	}

	planner, err := investigation.NewModelPlanner(investigation.ModelConfig{
		Completer:           client,
		MaxProbes:           cfg.LLMMaxProbes,
		MaxProbeScriptBytes: 2048,
		MaxObservationBytes: cfg.LLMMaxObservationBytes,
		MaxRequestBytes:     8192,
		MaxPromptBytes:      128 << 10,
		MaxTokens:           cfg.LLMMaxTokens,
		DefaultRunAs:        runAs,
	})
	if err != nil {
		return nil, fmt.Errorf("build model planner: %w", err)
	}
	logger.Info("model planner configured", "model", cfg.LLMModel, "run_as", runAs, "agent_loop", cfg.AgentLoop)
	return planner, nil
}

// plannerMaxCalls gives the investigation shell a budget large enough for the
// configured planner. The shell still enforces the limit.
func plannerMaxCalls(cfg config.Config) int {
	if cfg.AgentLoop {
		calls := cfg.AgentRounds * cfg.AgentCallsPerRound
		if calls > 100 {
			return 100
		}
		return calls
	}
	if cfg.Planner == "model" {
		return cfg.LLMMaxProbes
	}
	return len(defaultProbes)
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
