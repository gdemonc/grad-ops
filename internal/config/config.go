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
	"strings"
	"time"
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

	defaultAddr      = "127.0.0.1:8080"
	defaultLog       = "info"
	defaultBash      = "/bin/bash"
	defaultWorkDir   = "/"
	defaultPolicy    = "2026-11-01"
	defaultRoot      = "/var/log"
	defaultTTL       = 15 * time.Minute
	defaultProbe     = 10 * time.Second
	defaultMaxOutput = 64 << 10
	maxBodyLimit     = 8 << 20
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
}

// Load reads the environment, validates it, and returns the configuration.
func Load() (Config, error) {
	cfg := Config{
		Addr:            getenv(envAddr, defaultAddr),
		DBPath:          getenv(envDBPath, filepath.Join("data", "gradops.db")),
		MaxRequestBytes: 1 << 20,
		Bash:            getenv(envBash, defaultBash),
		WorkDir:         getenv(envWorkDir, defaultWorkDir),
		ContractWorkDir: getenv(envContractWorkDir, "/"),
		PolicyVersion:   getenv(envPolicyVersion, defaultPolicy),
		ApprovalTTL:     defaultTTL,
		ProbeTimeout:    defaultProbe,
		MaxOutputBytes:  defaultMaxOutput,
		EnableWorker:    true,
	}

	level, err := parseLevel(getenv(envLogLevel, defaultLog))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	if _, _, err := net.SplitHostPort(cfg.Addr); err != nil {
		return Config{}, fmt.Errorf("%s must be a host:port address: %w", envAddr, err)
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

	if raw := strings.TrimSpace(os.Getenv(envMaxOutput)); raw != "" {
		var n int
		if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 || n > maxBodyLimit {
			return Config{}, fmt.Errorf("%s must be an integer between 1 and %d", envMaxOutput, maxBodyLimit)
		}
		cfg.MaxOutputBytes = n
	}

	if raw := strings.TrimSpace(os.Getenv(envRequireRunAs)); raw != "" {
		value, err := parseBool(envRequireRunAs, raw)
		if err != nil {
			return Config{}, err
		}
		cfg.RequireRunAsMatch = value
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
