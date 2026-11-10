package shell_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/shell"
)

func newConsole(t *testing.T) *shell.Console {
	t.Helper()

	console, err := shell.NewConsole(baseConfig(t))
	if err != nil {
		t.Fatalf("new console: %v", err)
	}
	t.Cleanup(func() { _ = console.Close() })
	return console
}

func TestConsoleKeepsStateBetweenCalls(t *testing.T) {
	console := newConsole(t)

	if _, err := console.Run(context.Background(), `export GRADOPS_MARK=kept; mkdir -p nested; cd nested`); err != nil {
		t.Fatalf("first call: %v", err)
	}

	result, err := console.Run(context.Background(), `printf '%s:%s' "$GRADOPS_MARK" "$PWD"`)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); !strings.HasPrefix(got, "kept:") {
		t.Fatalf("environment did not survive the call boundary: %q", got)
	}
	if got := strings.TrimSpace(result.Stdout); !strings.HasSuffix(got, "nested") {
		t.Fatalf("working directory did not survive the call boundary: %q", got)
	}
	if result.Call != 2 {
		t.Fatalf("expected call index 2, got %d", result.Call)
	}
}

func TestConsoleSeparatesStreamsAndExitCode(t *testing.T) {
	console := newConsole(t)

	result, err := console.Run(context.Background(), `printf 'out'; printf 'err' >&2; false`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Stdout != "out" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if result.Stderr != "err" {
		t.Fatalf("stderr = %q", result.Stderr)
	}
	if result.ExitCode != 1 {
		t.Fatalf("exit code = %d, want 1", result.ExitCode)
	}
	if !console.Alive() {
		t.Fatalf("a failing script must not kill the console")
	}

	next, err := console.Run(context.Background(), `printf 'still here'`)
	if err != nil || next.Stdout != "still here" {
		t.Fatalf("console unusable after a failed call: %v (%q)", err, next.Stdout)
	}
}

func TestConsoleTruncatesEachStream(t *testing.T) {
	cfg := baseConfig(t)
	cfg.MaxOutputBytes = 64
	console, err := shell.NewConsole(cfg)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}
	defer func() { _ = console.Close() }()

	result, err := console.Run(context.Background(), `for i in $(seq 1 500); do printf 'x'; done; printf 'yy' >&2`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(result.Stdout) != 64 || !result.StdoutTruncated {
		t.Fatalf("stdout should be capped and reported: len=%d truncated=%t", len(result.Stdout), result.StdoutTruncated)
	}
	if result.Stderr != "yy" || result.StderrTruncated {
		t.Fatalf("stderr = %q truncated=%t", result.Stderr, result.StderrTruncated)
	}
}

func TestConsoleTimeoutKillsTheShell(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Timeout = 500 * time.Millisecond
	console, err := shell.NewConsole(cfg)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}
	defer func() { _ = console.Close() }()

	result, err := console.Run(context.Background(), `sleep 30`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("expected a timeout")
	}
	if console.Alive() {
		t.Fatalf("a timed-out console must be marked dead: its shell may be anywhere")
	}

	// The dead console is not silently restarted: the caller decides, because
	// state built up by earlier calls is gone either way.
	if _, err := console.Run(context.Background(), `printf ok`); !errors.Is(err, shell.ErrConsoleDead) {
		t.Fatalf("expected ErrConsoleDead, got %v", err)
	}
}

func TestConsoleReportsExitedShell(t *testing.T) {
	console := newConsole(t)

	_, err := console.Run(context.Background(), `exit 0`)
	if !errors.Is(err, shell.ErrConsoleDead) {
		t.Fatalf("a script that ends the shell must be reported, got %v", err)
	}
	if console.Alive() {
		t.Fatalf("console should be dead after the shell exits")
	}
}

func TestConsoleEnforcesCallBudget(t *testing.T) {
	cfg := baseConfig(t)
	cfg.MaxCalls = 2
	console, err := shell.NewConsole(cfg)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}
	defer func() { _ = console.Close() }()

	for i := 0; i < 2; i++ {
		if _, err := console.Run(context.Background(), `true`); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := console.Run(context.Background(), `true`); !errors.Is(err, shell.ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
}

func TestConsoleCloseIsIdempotent(t *testing.T) {
	console := newConsole(t)
	if _, err := console.Run(context.Background(), `true`); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := console.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := console.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := console.Run(context.Background(), `true`); !errors.Is(err, shell.ErrConsoleDead) {
		t.Fatalf("expected ErrConsoleDead after close, got %v", err)
	}
}

func TestNewConsoleRejectsUnusableConfig(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Timeout = 0
	if _, err := shell.NewConsole(cfg); !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}
func TestConsoleUsesTheConfiguredScratchDirectory(t *testing.T) {
	scratch := t.TempDir()
	cfg := baseConfig(t)
	cfg.ScratchDir = scratch

	console, err := shell.NewConsole(cfg)
	if err != nil {
		t.Fatalf("new console: %v", err)
	}

	if _, err := console.Run(context.Background(), `printf ok`); err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = console.Close()

	// The console removes its scratch directory when it closes, so the
	// configured directory itself must remain and be empty.
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("read scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("console left files behind: %v", entries)
	}
}

func TestConsoleRejectsMissingScratchDirectory(t *testing.T) {
	cfg := baseConfig(t)
	cfg.ScratchDir = cfg.WorkDir + string(os.PathSeparator) + "does-not-exist"
	if _, err := shell.NewConsole(cfg); !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}
