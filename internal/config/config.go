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
)

// Environment variable names. They are prefixed with GRADOPS_ so a deployment
// can override them without touching the service definition.
const (
	envAddr      = "GRADOPS_ADDR"
	envDBPath    = "GRADOPS_DB_PATH"
	envLogLevel  = "GRADOPS_LOG_LEVEL"
	envMaxBody   = "GRADOPS_MAX_REQUEST_BYTES"
	defaultAddr  = "127.0.0.1:8080"
	defaultLog   = "info"
	maxBodyLimit = 8 << 20
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
}

// Load reads the environment, validates it, and returns the configuration.
func Load() (Config, error) {
	cfg := Config{
		Addr:            getenv(envAddr, defaultAddr),
		DBPath:          getenv(envDBPath, filepath.Join("data", "gradops.db")),
		MaxRequestBytes: 1 << 20,
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
