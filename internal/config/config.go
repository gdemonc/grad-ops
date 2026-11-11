// Package config loads and validates the process configuration.
//
// Configuration comes from the environment only. Every value is validated
// before the service starts: an invalid or ambiguous configuration must fail
// closed instead of falling back to an implicit default.
package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/shell"
)

// Environment variable names. They are prefixed with GRADOPS_ so a deployment
// can override them without touching the service definition.
const (
	envAddr            = "GRADOPS_ADDR"
	envDBPath          = "GRADOPS_DB_PATH"
	envLogLevel        = "GRADOPS_LOG_LEVEL"
	envMaxBody         = "GRADOPS_MAX_REQUEST_BYTES"
	envBash            = "GRADOPS_BASH"
	envWorkDir         = "GRADOPS_WORK_DIR"
	envContractWorkDir = "GRADOPS_CONTRACT_WORK_DIR"
	envAllowedRoots    = "GRADOPS_ALLOWED_ROOTS"
	envPolicyVersion   = "GRADOPS_POLICY_VERSION"
	envApprovalTTL     = "GRADOPS_APPROVAL_TTL"
	envWorker          = "GRADOPS_WORKER"
	envProbeTimeout    = "GRADOPS_PROBE_TIMEOUT"
	envMaxOutput       = "GRADOPS_MAX_OUTPUT_BYTES"
	envRequireRunAs    = "GRADOPS_REQUIRE_RUN_AS_MATCH"
	envProbeConsole    = "GRADOPS_PROBE_CONSOLE"
	envInvestigation   = "GRADOPS_INVESTIGATION_TIMEOUT"
	envExecution       = "GRADOPS_EXECUTION_TIMEOUT"
	envRuntime         = "GRADOPS_RUNTIME"
	envCgroupRoot      = "GRADOPS_CGROUP_ROOT"
	envCgroupMemory    = "GRADOPS_CGROUP_MEMORY_MAX_BYTES"
	envCgroupPids      = "GRADOPS_CGROUP_PIDS_MAX"
	envCgroupCPUQuota  = "GRADOPS_CGROUP_CPU_QUOTA_MICROS"
	envCgroupCPUPeriod = "GRADOPS_CGROUP_CPU_PERIOD_MICROS"
	envSandbox         = "GRADOPS_SANDBOX"
	envBubblewrap      = "GRADOPS_BWRAP"
	envSandboxNetwork  = "GRADOPS_SANDBOX_NETWORK"
	envAPIToken        = "GRADOPS_API_TOKEN"
	envExecutorUser    = "GRADOPS_EXECUTOR_USER"
	envPrlimit         = "GRADOPS_PRLIMIT"
	envLimitCPU        = "GRADOPS_RLIMIT_CPU"
	envLimitAS         = "GRADOPS_RLIMIT_AS_BYTES"
	envLimitNProc      = "GRADOPS_RLIMIT_NPROC"
	envLimitFSize      = "GRADOPS_RLIMIT_FSIZE_BYTES"
	envLimitNoFile     = "GRADOPS_RLIMIT_NOFILE"
	envPlanner         = "GRADOPS_PLANNER"
	envAgentLoop       = "GRADOPS_AGENT_LOOP"
	envAgentRounds     = "GRADOPS_AGENT_MAX_ROUNDS"
	envAgentMaxCalls   = "GRADOPS_AGENT_MAX_CALLS"
	envAgentCalls      = "GRADOPS_AGENT_MAX_CALLS_PER_ROUND"
	envAgentContext    = "GRADOPS_AGENT_CONTEXT_BYTES"
	envLLMBaseURL      = "GRADOPS_LLM_BASE_URL"
	envLLMAPIKey       = "GRADOPS_LLM_API_KEY"
	envLLMModel        = "GRADOPS_LLM_MODEL"
	envLLMTimeout      = "GRADOPS_LLM_TIMEOUT"
	envLLMResponse     = "GRADOPS_LLM_MAX_RESPONSE_BYTES"
	envLLMMaxProbes    = "GRADOPS_LLM_MAX_PROBES"
	envLLMMaxObs       = "GRADOPS_LLM_MAX_OBSERVATION_BYTES"
	envLLMMaxTokens    = "GRADOPS_LLM_MAX_TOKENS"

	defaultAddr          = "127.0.0.1:8080"
	defaultLog           = "info"
	defaultBash          = "/bin/bash"
	defaultWorkDir       = "/"
	defaultPolicy        = "2026-11-01"
	defaultRoot          = "/var/log"
	defaultTTL           = 15 * time.Minute
	defaultProbe         = 10 * time.Second
	defaultInvestigation = 5 * time.Minute
	defaultExecution     = 5 * time.Minute
	defaultRuntime       = "direct"
	defaultCgroupMemory  = 512 << 20
	defaultCgroupPids    = 256
	defaultCgroupPeriod  = 100_000
	defaultSandbox       = "none"
	defaultBubblewrap    = "/usr/bin/bwrap"
	defaultMaxOutput     = 64 << 10
	defaultPrlimit       = "/usr/bin/prlimit"
	maxBodyLimit         = 8 << 20
	maxByteLimit         = 1 << 40
	maxCountLimit        = 1_000_000
	defaultPlanner       = "stub"
	defaultAgentRounds   = 20
	defaultAgentMaxCalls = 40
	defaultAgentCalls    = 5
	defaultAgentContext  = 32 << 10
	defaultLLMTimeout    = 30 * time.Second
	defaultLLMResponse   = 1 << 20
	defaultLLMMaxProbes  = 6
	defaultLLMMaxObs     = 4096
	defaultLLMMaxTokens  = 1024
)

// Default resource limits. They are deliberately generous enough for ordinary
// observation commands and small files, and finite enough that a runaway script
// cannot take the host with it. A zero value disables one limit; all zeroes
// disable prlimit entirely.
const (
	defaultLimitCPU    = 10 * time.Second
	defaultLimitAS     = 512 << 20
	defaultLimitNProc  = 256
	defaultLimitFSize  = 16 << 20
	defaultLimitNoFile = 256
)

// Config is the validated service configuration.
type Config struct {
	// Addr is the listen address in host:port form.
	Addr string
	// DBPath is the SQLite database file.
	DBPath string
	// DataDir is the directory that holds the database and other service data.
	DataDir string
	// LogLevel is the minimum level written to the log.
	LogLevel slog.Level
	// MaxRequestBytes bounds the size of an accepted JSON request body.
	MaxRequestBytes int64
	// APIToken is the bearer token required for /api when non-empty. Without
	// one, the listener must be loopback-only.
	APIToken string

	// Bash is the absolute path of the shell used for probes and actions.
	Bash string
	// WorkDir is the host directory probes and actions run in.
	WorkDir string
	// ContractWorkDir is the POSIX path recorded in contracts. It differs from
	// WorkDir only on a development host, where the two cannot be the same.
	ContractWorkDir string
	// AllowedRoots bounds every path an action may declare.
	AllowedRoots []string
	// PolicyVersion identifies the rule set that produced a decision.
	PolicyVersion string
	// ApprovalTTL is how long an approval token stays valid.
	ApprovalTTL time.Duration
	// EnableWorker runs the durable job loop in this process.
	EnableWorker bool
	// ProbeTimeout bounds a single investigation probe.
	ProbeTimeout time.Duration
	// MaxOutputBytes bounds each captured stream.
	MaxOutputBytes int
	// RequireRunAsMatch refuses actions whose declared identity this process
	// cannot assume. Off by default because the service user is the identity
	// used on a host without a runtime that can switch.
	RequireRunAsMatch bool
	// PersistentProbes runs an investigation's probes in one Bash process, so
	// they may share state. Off by default: independent probes are the clearer
	// evidence.
	PersistentProbes bool
	// InvestigationTimeout bounds one task's investigation across probes and
	// model calls.
	InvestigationTimeout time.Duration
	// ExecutionTimeout bounds one approved action's execution. It is an
	// emergency ceiling on top of the contract's own runtime timeout.
	ExecutionTimeout time.Duration
	// RuntimeMode selects the per-shell Linux runtime: direct or cgroup-v2.
	RuntimeMode string
	// Cgroup configures the cgroup v2 boundary when RuntimeMode is cgroup-v2.
	Cgroup shell.CgroupConfig
	// SandboxMode selects none or bubblewrap.
	SandboxMode string
	// Bubblewrap is the absolute path of the bubblewrap binary.
	Bubblewrap string
	// SandboxNetwork keeps the host network namespace when true.
	SandboxNetwork bool
	// ExecutorUser is the Linux identity that probes, actions and verification
	// run as. Empty keeps the service identity; otherwise every shell must
	// actually assume this identity or startup fails.
	ExecutorUser string
	// Limits bounds every shell process. It is applied with prlimit.
	Limits shell.Limits
	// Prlimit is the path of util-linux prlimit. It is ignored when Limits is
	// zero.
	Prlimit string
	// Planner selects "stub" (deterministic probes) or "model" (an
	// OpenAI-compatible provider).
	Planner string
	// AgentLoop selects the multi-round observe/finish investigation loop.
	AgentLoop bool
	// AgentRounds, AgentMaxCalls and AgentCallsPerRound bound the loop.
	AgentRounds        int
	AgentMaxCalls      int
	AgentCallsPerRound int
	// AgentContextBytes bounds the observation context sent to the model.
	AgentContextBytes int
	// LLMBaseURL, LLMAPIKey and LLMModel configure the provider. The key is
	// never logged or written to the database.
	LLMBaseURL string
	LLMAPIKey  string
	LLMModel   string
	// LLMTimeout bounds one provider request.
	LLMTimeout time.Duration
	// LLMMaxResponseBytes bounds the provider's response body.
	LLMMaxResponseBytes int64
	// LLMMaxProbes bounds the first model phase.
	LLMMaxProbes int
	// LLMMaxObservationBytes bounds one observation in the conclusion prompt.
	LLMMaxObservationBytes int
	// LLMMaxTokens bounds the provider's completion.
	LLMMaxTokens int
}

// Load reads the environment, validates it, and returns the configuration.
func Load() (Config, error) {
	cfg := Config{
		Addr:                 getenv(envAddr, defaultAddr),
		DBPath:               getenv(envDBPath, filepath.Join("data", "gradops.db")),
		MaxRequestBytes:      1 << 20,
		APIToken:             strings.TrimSpace(os.Getenv(envAPIToken)),
		Bash:                 getenv(envBash, defaultBash),
		WorkDir:              getenv(envWorkDir, defaultWorkDir),
		ContractWorkDir:      getenv(envContractWorkDir, "/"),
		PolicyVersion:        getenv(envPolicyVersion, defaultPolicy),
		ApprovalTTL:          defaultTTL,
		ProbeTimeout:         defaultProbe,
		InvestigationTimeout: defaultInvestigation,
		ExecutionTimeout:     defaultExecution,
		RuntimeMode:          strings.ToLower(getenv(envRuntime, defaultRuntime)),
		Cgroup: shell.CgroupConfig{
			Root:            getenv(envCgroupRoot, ""),
			MemoryMaxBytes:  defaultCgroupMemory,
			PidsMax:         defaultCgroupPids,
			CPUPeriodMicros: defaultCgroupPeriod,
		},
		SandboxMode:    strings.ToLower(getenv(envSandbox, defaultSandbox)),
		Bubblewrap:     getenv(envBubblewrap, defaultBubblewrap),
		MaxOutputBytes: defaultMaxOutput,
		EnableWorker:   true,
		ExecutorUser:   getenv(envExecutorUser, ""),
		Prlimit:        getenv(envPrlimit, defaultPrlimit),
		Limits: shell.Limits{
			CPU:               defaultLimitCPU,
			AddressSpaceBytes: defaultLimitAS,
			MaxProcesses:      defaultLimitNProc,
			MaxFileSizeBytes:  defaultLimitFSize,
			MaxOpenFiles:      defaultLimitNoFile,
		},
		Planner:                getenv(envPlanner, defaultPlanner),
		AgentRounds:            defaultAgentRounds,
		AgentMaxCalls:          defaultAgentMaxCalls,
		AgentCallsPerRound:     defaultAgentCalls,
		AgentContextBytes:      defaultAgentContext,
		LLMBaseURL:             getenv(envLLMBaseURL, ""),
		LLMAPIKey:              strings.TrimSpace(os.Getenv(envLLMAPIKey)),
		LLMModel:               getenv(envLLMModel, ""),
		LLMTimeout:             defaultLLMTimeout,
		LLMMaxResponseBytes:    defaultLLMResponse,
		LLMMaxProbes:           defaultLLMMaxProbes,
		LLMMaxObservationBytes: defaultLLMMaxObs,
		LLMMaxTokens:           defaultLLMMaxTokens,
	}

	level, err := parseLevel(getenv(envLogLevel, defaultLog))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return Config{}, fmt.Errorf("%s must be a host:port address: %w", envAddr, err)
	}
	if cfg.APIToken != "" && len(cfg.APIToken) < 16 {
		return Config{}, fmt.Errorf("%s must be at least 16 characters", envAPIToken)
	}
	if cfg.APIToken == "" && !loopbackAddr(cfg.Addr) {
		return Config{}, fmt.Errorf("%s must bind to a loopback address unless %s is set", envAddr, envAPIToken)
	}
	if strings.TrimSpace(cfg.DBPath) == "" {
		return Config{}, fmt.Errorf("%s must not be empty", envDBPath)
	}
	cfg.DataDir = filepath.Dir(cfg.DBPath)

	if raw := strings.TrimSpace(os.Getenv(envMaxBody)); raw != "" {
		var n int64
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 || n > maxBodyLimit {
			return Config{}, fmt.Errorf("%s must be an integer between 1 and %d", envMaxBody, maxBodyLimit)
		}
		cfg.MaxRequestBytes = n
	}

	if !filepath.IsAbs(cfg.Bash) {
		return Config{}, fmt.Errorf("%s must be an absolute path", envBash)
	}
	if info, err := os.Stat(cfg.Bash); err != nil || info.IsDir() {
		return Config{}, fmt.Errorf("%s must point at an executable file", envBash)
	}

	if !filepath.IsAbs(cfg.WorkDir) {
		return Config{}, fmt.Errorf("%s must be an absolute path", envWorkDir)
	}
	if info, err := os.Stat(cfg.WorkDir); err != nil || !info.IsDir() {
		return Config{}, fmt.Errorf("%s must be an existing directory", envWorkDir)
	}
	if !strings.HasPrefix(cfg.ContractWorkDir, "/") {
		return Config{}, fmt.Errorf("%s must be an absolute POSIX path", envContractWorkDir)
	}

	roots := strings.Split(getenv(envAllowedRoots, defaultRoot), ":")
	cfg.AllowedRoots = cfg.AllowedRoots[:0]
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if !strings.HasPrefix(root, "/") {
			return Config{}, fmt.Errorf("%s entries must be absolute POSIX paths (got %q)", envAllowedRoots, root)
		}
		cfg.AllowedRoots = append(cfg.AllowedRoots, root)
	}
	if len(cfg.AllowedRoots) == 0 {
		return Config{}, fmt.Errorf("%s must list at least one root", envAllowedRoots)
	}

	if raw := strings.TrimSpace(os.Getenv(envApprovalTTL)); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil || ttl <= 0 || ttl > 24*time.Hour {
			return Config{}, fmt.Errorf("%s must be a duration between 1s and 24h", envApprovalTTL)
		}
		cfg.ApprovalTTL = ttl
	}

	if raw := strings.TrimSpace(os.Getenv(envProbeTimeout)); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 5*time.Minute {
			return Config{}, fmt.Errorf("%s must be a duration between 1s and 5m", envProbeTimeout)
		}
		cfg.ProbeTimeout = timeout
	}

	if raw := strings.TrimSpace(os.Getenv(envInvestigation)); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 30*time.Minute {
			return Config{}, fmt.Errorf("%s must be a duration between 1ns and 30m", envInvestigation)
		}
		cfg.InvestigationTimeout = timeout
	}

	if raw := strings.TrimSpace(os.Getenv(envExecution)); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 24*time.Hour {
			return Config{}, fmt.Errorf("%s must be a duration between 1ns and 24h", envExecution)
		}
		cfg.ExecutionTimeout = timeout
	}

	switch cfg.SandboxMode {
	case "none", "bubblewrap":
	default:
		return Config{}, fmt.Errorf("%s must be none or bubblewrap", envSandbox)
	}
	if raw := strings.TrimSpace(os.Getenv(envSandboxNetwork)); raw != "" {
		value, err := parseBool(envSandboxNetwork, raw)
		if err != nil {
			return Config{}, err
		}
		cfg.SandboxNetwork = value
	}
	if cfg.SandboxMode == "bubblewrap" && !filepath.IsAbs(cfg.Bubblewrap) {
		return Config{}, fmt.Errorf("%s must be an absolute path", envBubblewrap)
	}

	switch cfg.RuntimeMode {
	case "direct":
		cfg.Cgroup.Enabled = false
	case "cgroup-v2":
		cfg.Cgroup.Enabled = true
	default:
		return Config{}, fmt.Errorf("%s must be direct or cgroup-v2", envRuntime)
	}
	cgroupMemory, err := parseBound(envCgroupMemory, cfg.Cgroup.MemoryMaxBytes, maxByteLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Cgroup.MemoryMaxBytes = cgroupMemory
	cgroupPids, err := parseBound(envCgroupPids, cfg.Cgroup.PidsMax, maxCountLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Cgroup.PidsMax = cgroupPids
	cgroupQuota, err := parseBound(envCgroupCPUQuota, cfg.Cgroup.CPUQuotaMicros, maxCountLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Cgroup.CPUQuotaMicros = cgroupQuota
	cgroupPeriod, err := parseBound(envCgroupCPUPeriod, cfg.Cgroup.CPUPeriodMicros, maxCountLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Cgroup.CPUPeriodMicros = cgroupPeriod

	if raw := strings.TrimSpace(os.Getenv(envMaxOutput)); raw != "" {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 || n > maxBodyLimit {
			return Config{}, fmt.Errorf("%s must be an integer between 1 and %d", envMaxOutput, maxBodyLimit)
		}
		cfg.MaxOutputBytes = n
	}

	if raw := strings.TrimSpace(os.Getenv(envLimitCPU)); raw != "" {
		if raw == "0" {
			cfg.Limits.CPU = 0
		} else {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 || value > 24*time.Hour {
				return Config{}, fmt.Errorf("%s must be 0 or a duration between 1ns and 24h", envLimitCPU)
			}
			cfg.Limits.CPU = value
		}
	}

	addressSpace, err := parseBound(envLimitAS, cfg.Limits.AddressSpaceBytes, maxByteLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Limits.AddressSpaceBytes = addressSpace

	maxProcesses, err := parseBound(envLimitNProc, cfg.Limits.MaxProcesses, maxCountLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Limits.MaxProcesses = maxProcesses

	maxFileSize, err := parseBound(envLimitFSize, cfg.Limits.MaxFileSizeBytes, maxByteLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Limits.MaxFileSizeBytes = maxFileSize

	maxOpenFiles, err := parseBound(envLimitNoFile, cfg.Limits.MaxOpenFiles, maxCountLimit)
	if err != nil {
		return Config{}, err
	}
	cfg.Limits.MaxOpenFiles = maxOpenFiles

	if cfg.ExecutorUser != "" {
		if cfg.ExecutorUser == "0" || strings.EqualFold(cfg.ExecutorUser, "root") {
			return Config{}, fmt.Errorf("%s must not be root", envExecutorUser)
		}
	}

	cfg.Planner = strings.ToLower(strings.TrimSpace(cfg.Planner))
	switch cfg.Planner {
	case "stub", "model":
	default:
		return Config{}, fmt.Errorf("%s must be stub or model (got %q)", envPlanner, cfg.Planner)
	}
	if raw := strings.TrimSpace(os.Getenv(envAgentLoop)); raw != "" {
		value, err := parseBool(envAgentLoop, raw)
		if err != nil {
			return Config{}, err
		}
		cfg.AgentLoop = value
	}
	if raw := strings.TrimSpace(os.Getenv(envAgentRounds)); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 2 || value > 20 {
			return Config{}, fmt.Errorf("%s must be an integer between 2 and 20", envAgentRounds)
		}
		cfg.AgentRounds = value
	}
	if raw := strings.TrimSpace(os.Getenv(envAgentMaxCalls)); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 200 {
			return Config{}, fmt.Errorf("%s must be an integer between 1 and 200", envAgentMaxCalls)
		}
		cfg.AgentMaxCalls = value
	}
	if raw := strings.TrimSpace(os.Getenv(envAgentCalls)); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 8 {
			return Config{}, fmt.Errorf("%s must be an integer between 1 and 8", envAgentCalls)
		}
		cfg.AgentCallsPerRound = value
	}
	contextBytes, err := parseBound(envAgentContext, int64(cfg.AgentContextBytes), 1<<20)
	if err != nil {
		return Config{}, err
	}
	if contextBytes < 1024 {
		return Config{}, fmt.Errorf("%s must be at least 1024", envAgentContext)
	}
	cfg.AgentContextBytes = int(contextBytes)
	if cfg.AgentLoop && cfg.Planner != "model" {
		return Config{}, fmt.Errorf("%s=true requires %s=model", envAgentLoop, envPlanner)
	}
	if cfg.AgentCallsPerRound > cfg.AgentMaxCalls {
		return Config{}, fmt.Errorf("%s must not exceed %s", envAgentCalls, envAgentMaxCalls)
	}

	if cfg.Planner == "model" {
		if cfg.LLMBaseURL == "" {
			return Config{}, fmt.Errorf("%s is required when %s=model", envLLMBaseURL, envPlanner)
		}
		if cfg.LLMModel == "" {
			return Config{}, fmt.Errorf("%s is required when %s=model", envLLMModel, envPlanner)
		}
	}

	if raw := strings.TrimSpace(os.Getenv(envLLMTimeout)); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout <= 0 || timeout > 5*time.Minute {
			return Config{}, fmt.Errorf("%s must be a duration between 1ns and 5m", envLLMTimeout)
		}
		cfg.LLMTimeout = timeout
	}
	responseBytes, err := parseBound(envLLMResponse, cfg.LLMMaxResponseBytes, 8<<20)
	if err != nil {
		return Config{}, err
	}
	cfg.LLMMaxResponseBytes = responseBytes
	maxProbes, err := parseBound(envLLMMaxProbes, int64(cfg.LLMMaxProbes), 12)
	if err != nil {
		return Config{}, err
	}
	cfg.LLMMaxProbes = int(maxProbes)
	maxObservation, err := parseBound(envLLMMaxObs, int64(cfg.LLMMaxObservationBytes), 64<<10)
	if err != nil {
		return Config{}, err
	}
	cfg.LLMMaxObservationBytes = int(maxObservation)
	maxTokens, err := parseBound(envLLMMaxTokens, int64(cfg.LLMMaxTokens), 8192)
	if err != nil {
		return Config{}, err
	}
	cfg.LLMMaxTokens = int(maxTokens)
	if cfg.LLMMaxResponseBytes <= 0 || cfg.LLMMaxProbes <= 0 ||
		cfg.LLMMaxObservationBytes <= 0 || cfg.LLMMaxTokens <= 0 {
		return Config{}, fmt.Errorf("LLM limits must be positive")
	}

	if raw := strings.TrimSpace(os.Getenv(envRequireRunAs)); raw != "" {
		value, err := parseBool(envRequireRunAs, raw)
		if err != nil {
			return Config{}, err
		}
		cfg.RequireRunAsMatch = value
	}

	if raw := strings.TrimSpace(os.Getenv(envProbeConsole)); raw != "" {
		value, err := parseBool(envProbeConsole, raw)
		if err != nil {
			return Config{}, err
		}
		cfg.PersistentProbes = value
	}

	if raw := strings.TrimSpace(os.Getenv(envWorker)); raw != "" {
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			cfg.EnableWorker = true
		case "0", "false", "no", "off":
			cfg.EnableWorker = false
		default:
			return Config{}, fmt.Errorf("%s must be a boolean (got %q)", envWorker, raw)
		}
	}

	return cfg, nil
}

// loopbackAddr reports whether a host:port address is provably local. A
// hostname that is not localhost is not resolved: DNS could make the check
// depend on the resolver, so it is treated as non-loopback.
func loopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func getenv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s must be one of debug, info, warn, error (got %q)", envLogLevel, raw)
	}
}
func parseBool(name, raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean (got %q)", name, raw)
	}
}

// parseBound reads a non-negative integer. Zero means "no limit", which is
// distinct from the fallback used when the variable is unset.
func parseBound(name string, fallback, maximum int64) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 || value > maximum {
		return 0, fmt.Errorf("%s must be an integer between 0 and %d", name, maximum)
	}
	return value, nil
}
