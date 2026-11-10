// Package shell runs bounded, non-interactive Bash scripts in a controlled
// environment.
//
// A session provides expression — pipes, redirection, command substitution,
// multi-line scripts — and the bookkeeping that keeps it bounded: a per-call
// timeout, capped output, and a call budget. It grants no authority. Whether a
// script may run at all is decided by policy, and what it can touch is bounded
// by the runtime that starts it.
package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	// ErrBudgetExhausted means the session's call budget is used up.
	ErrBudgetExhausted = errors.New("shell call budget exhausted")
	// ErrInvalidConfig means the session was configured with unusable values.
	ErrInvalidConfig = errors.New("invalid shell configuration")
)

// killGrace bounds how long a killed call may hold its output pipes open before
// the session gives up waiting for it.
const killGrace = 2 * time.Second

// Config describes one session.
type Config struct {
	// Executable is the absolute path of the Bash binary. It is never resolved
	// through PATH: the caller decides which shell runs.
	Executable string
	// WorkDir is the session's working directory.
	WorkDir string
	// Env is the environment the script sees. Variables of the service process
	// are not inherited, so credentials in its environment cannot leak into a
	// script. Bash still contributes its own defaults (PATH, PWD, SHLVL, _),
	// so callers that need a fixed PATH must set it here.
	Env map[string]string
	// Timeout bounds a single call.
	Timeout time.Duration
	// MaxOutputBytes bounds each captured stream independently.
	MaxOutputBytes int
	// MaxCalls bounds how many scripts the session may run.
	MaxCalls int
	// ScratchDir is where a Console keeps the per-call capture files. Empty
	// means the operating system's temporary directory, which is not
	// necessarily writable by a service account; deployments that run a service
	// should point it at a directory the service owns.
	ScratchDir string
	// RunAs is the Linux identity the shell runs as. Empty keeps the service
	// identity. A non-empty value is resolved and, when the process is allowed
	// to switch, applied with setuid/setgid before Bash starts.
	RunAs string
	// Limits bounds the shell process. Zero fields leave the inherited limit
	// unchanged. Applying any limit requires Prlimit.
	Limits Limits
	// Prlimit is the absolute path of util-linux prlimit. It defaults to
	// /usr/bin/prlimit when Limits is non-zero.
	Prlimit string
	// Cgroup, when enabled, starts the shell in a fresh cgroup v2 child with
	// memory, process and CPU limits.
	Cgroup CgroupConfig
}

// Result is the outcome of one call.
type Result struct {
	Script          string
	ExitCode        int
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	TimedOut        bool
	Duration        time.Duration
	// Call is the 1-based index of this call within the session.
	Call int
}

// Truncated reports whether either stream was cut short.
func (r Result) Truncated() bool { return r.StdoutTruncated || r.StderrTruncated }

// Session is a bounded sequence of Bash calls sharing configuration and budget.
type Session struct {
	cfg Config

	mu    sync.Mutex
	calls int
}

// NewSession validates cfg and returns a session.
func NewSession(cfg Config) (*Session, error) {
	cfg, err := prepareConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Session{cfg: cfg}, nil
}

// validateConfig holds the checks a session and a console both need.
func validateConfig(cfg Config) error {
	if !filepath.IsAbs(cfg.Executable) {
		return fmt.Errorf("%w: executable must be an absolute path", ErrInvalidConfig)
	}
	info, err := os.Stat(cfg.Executable)
	if err != nil {
		return fmt.Errorf("%w: executable %q: %v", ErrInvalidConfig, cfg.Executable, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: executable %q is a directory", ErrInvalidConfig, cfg.Executable)
	}
	if cfg.WorkDir == "" {
		return fmt.Errorf("%w: work directory must not be empty", ErrInvalidConfig)
	}
	if info, err := os.Stat(cfg.WorkDir); err != nil || !info.IsDir() {
		return fmt.Errorf("%w: work directory %q is not a directory", ErrInvalidConfig, cfg.WorkDir)
	}
	if cfg.Timeout <= 0 {
		return fmt.Errorf("%w: timeout must be positive", ErrInvalidConfig)
	}
	if cfg.MaxOutputBytes <= 0 {
		return fmt.Errorf("%w: output limit must be positive", ErrInvalidConfig)
	}
	if cfg.MaxCalls <= 0 {
		return fmt.Errorf("%w: call budget must be positive", ErrInvalidConfig)
	}
	if cfg.ScratchDir != "" {
		info, err := os.Stat(cfg.ScratchDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("%w: scratch directory %q is not a directory", ErrInvalidConfig, cfg.ScratchDir)
		}
	}
	if err := cfg.Cgroup.validate(); err != nil {
		return err
	}
	for key := range cfg.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("%w: invalid environment key %q", ErrInvalidConfig, key)
		}
		if strings.ContainsAny(cfg.Env[key], "\x00") {
			return fmt.Errorf("%w: environment value for %q contains NUL", ErrInvalidConfig, key)
		}
	}

	return nil
}

// Calls reports how many scripts the session has run.
func (s *Session) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// Remaining reports how many calls are left.
func (s *Session) Remaining() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.MaxCalls - s.calls
}

// Run executes one script.
//
// The script is passed to Bash as a single argument, so it is not word-split or
// re-interpreted by the caller's shell. Its effects are bounded by the timeout,
// the output caps and the process control the runtime installed.
func (s *Session) Run(ctx context.Context, script string) (Result, error) {
	if strings.TrimSpace(script) == "" {
		return Result{}, fmt.Errorf("%w: script must not be empty", ErrInvalidConfig)
	}

	s.mu.Lock()
	if s.calls >= s.cfg.MaxCalls {
		s.mu.Unlock()
		return Result{}, ErrBudgetExhausted
	}
	s.calls++
	call := s.calls
	s.mu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()

	command, err := newShellCommand(s.cfg, "--noprofile", "--norc", "-c", script)
	if err != nil {
		return Result{}, fmt.Errorf("prepare shell: %w", err)
	}
	guard, err := newCgroupGuard(s.cfg.Cgroup)
	if err != nil {
		return Result{}, fmt.Errorf("prepare cgroup: %w", err)
	}
	defer func() { _ = guard.close() }()
	if err := guard.attach(command); err != nil {
		return Result{}, fmt.Errorf("attach cgroup: %w", err)
	}
	stdout := newCappedBuffer(s.cfg.MaxOutputBytes)
	stderr := newCappedBuffer(s.cfg.MaxOutputBytes)
	command.Stdout = stdout
	command.Stderr = stderr

	started := time.Now()
	if err := command.Start(); err != nil {
		return Result{}, fmt.Errorf("start shell: %w", err)
	}

	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()

	var waitErr error
	timedOut := false
	select {
	case waitErr = <-waited:
	case <-callCtx.Done():
		timedOut = true
		// Kill the whole cgroup and process group: a script that backgrounded
		// work, or escaped its process group, must not outlive its turn.
		guard.kill()
		killProcess(command)
		select {
		case waitErr = <-waited:
		case <-time.After(killGrace):
			// A descendant survived the kill and still holds the output pipe.
			// Waiting forever would block the session on someone else's bug, so
			// the caller gets a timeout and the process is left to the OS.
			waitErr = nil
		}
	}

	result := Result{
		Script:          script,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		Duration:        time.Since(started),
		Call:            call,
	}

	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if timedOut {
		result.TimedOut = true
		result.ExitCode = -1
		return result, nil
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
			return result, nil
		}
		return result, fmt.Errorf("run shell: %w", waitErr)
	}

	return result, nil
}

// environ renders the configured environment deterministically.
func (s *Session) environ() []string { return environ(s.cfg.Env) }

func environ(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

// cappedBuffer keeps at most limit bytes and records whether more arrived.
//
// It never returns an error on overflow: a command that writes too much must be
// truncated and reported, not aborted mid-stream with a confusing failure.
type cappedBuffer struct {
	mu        sync.Mutex
	limit     int
	buf       bytes.Buffer
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.limit - b.buf.Len()
	switch {
	case remaining <= 0:
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	case len(p) > remaining:
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	default:
		b.buf.Write(p)
		return len(p), nil
	}
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
