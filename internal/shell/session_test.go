package shell_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/shell"
)

// requireBash locates a usable Bash and returns it with a scratch directory.
//
// The suite skips instead of failing when no shell can run, so a host without
// Bash reports "not verified" rather than a green tick that proves nothing.
func requireBash(t *testing.T) (string, string) {
	t.Helper()

	candidate := os.Getenv("GRADOPS_TEST_BASH")
	if candidate == "" {
		found, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("no bash on PATH; set GRADOPS_TEST_BASH to run these tests")
		}
		candidate = found
	}

	absolute, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("resolve bash path: %v", err)
	}

	workdir := t.TempDir()
	probe := exec.Command(absolute, "--noprofile", "--norc", "-c", "printf ok")
	output, err := probe.CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Skipf("bash at %s is not usable here: %v (%s)", absolute, err, output)
	}

	return absolute, workdir
}

func baseConfig(t *testing.T) shell.Config {
	t.Helper()
	bash, workdir := requireBash(t)
	return shell.Config{
		Executable:     bash,
		WorkDir:        workdir,
		Env:            map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		MaxCalls:       10,
	}
}

func TestRunCapturesOutputAndExitCode(t *testing.T) {
	session, err := shell.NewSession(baseConfig(t))
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := session.Run(context.Background(), `printf 'out'; printf 'err' >&2; exit 3`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", result.ExitCode)
	}
	if result.Stdout != "out" {
		t.Fatalf("unexpected stdout %q", result.Stdout)
	}
	if result.Stderr != "err" {
		t.Fatalf("unexpected stderr %q", result.Stderr)
	}
	if result.Call != 1 {
		t.Fatalf("expected call index 1, got %d", result.Call)
	}
	if result.Truncated() {
		t.Fatal("expected untruncated output")
	}
}

func TestRunSupportsPipesRedirectionAndSubstitution(t *testing.T) {
	cfg := baseConfig(t)
	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	script := `
set -eu
name=$(printf 'world')
printf 'hello %s\n' "$name" | tr 'a-z' 'A-Z' > "$HOME/out.txt"
cat "$HOME/out.txt"
`
	result, err := session.Run(context.Background(), script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("script failed: exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}
	if strings.TrimSpace(result.Stdout) != "HELLO WORLD" {
		t.Fatalf("unexpected stdout %q", result.Stdout)
	}
}

func TestRunTimesOutAndReportsIt(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Timeout = 300 * time.Millisecond
	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	started := time.Now()
	result, err := session.Run(context.Background(), `sleep 5`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.TimedOut {
		t.Fatal("expected the call to be reported as timed out")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRunTruncatesLargeOutput(t *testing.T) {
	cfg := baseConfig(t)
	cfg.MaxOutputBytes = 64
	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := session.Run(context.Background(), `seq 1 5000`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.StdoutTruncated {
		t.Fatal("expected stdout to be reported as truncated")
	}
	if len(result.Stdout) > cfg.MaxOutputBytes {
		t.Fatalf("captured %d bytes, limit was %d", len(result.Stdout), cfg.MaxOutputBytes)
	}
}

func TestRunEnforcesCallBudget(t *testing.T) {
	cfg := baseConfig(t)
	cfg.MaxCalls = 2
	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	for i := 0; i < 2; i++ {
		if _, err := session.Run(context.Background(), `true`); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := session.Run(context.Background(), `true`); !errors.Is(err, shell.ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	if session.Remaining() != 0 {
		t.Fatalf("expected no remaining calls, got %d", session.Remaining())
	}
}

// TestEnvironmentIsNotInherited is the property that keeps service credentials
// out of a script: variables from the service process do not exist in the
// shell. Bash still adds its own defaults (PATH, PWD, SHLVL), which is why the
// assertion probes a service variable rather than the whole environment.
func TestEnvironmentIsNotInherited(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Env = map[string]string{"HOME": cfg.WorkDir}
	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := session.Run(context.Background(), `printf '%s|%s' "${HOME:-empty}" "${GRADOPS_TEST_BASH:-unset}"`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	parts := strings.SplitN(result.Stdout, "|", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected output %q", result.Stdout)
	}
	if parts[0] == "empty" {
		t.Fatal("expected the configured HOME to reach the script")
	}
	if parts[1] != "unset" {
		t.Fatalf("service environment leaked into the shell: %q", parts[1])
	}
}

func TestNewSessionRejectsUnusableConfig(t *testing.T) {
	bash, workdir := requireBash(t)

	cases := map[string]func(*shell.Config){
		"relative executable": func(c *shell.Config) { c.Executable = "bash" },
		"missing executable":  func(c *shell.Config) { c.Executable = filepath.Join(workdir, "nope") },
		"missing workdir":     func(c *shell.Config) { c.WorkDir = filepath.Join(workdir, "nope") },
		"zero timeout":        func(c *shell.Config) { c.Timeout = 0 },
		"zero output limit":   func(c *shell.Config) { c.MaxOutputBytes = 0 },
		"zero budget":         func(c *shell.Config) { c.MaxCalls = 0 },
		"invalid env key":     func(c *shell.Config) { c.Env = map[string]string{"BAD=KEY": "x"} },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := shell.Config{
				Executable:     bash,
				WorkDir:        workdir,
				Env:            map[string]string{"PATH": os.Getenv("PATH")},
				Timeout:        time.Second,
				MaxOutputBytes: 128,
				MaxCalls:       1,
			}
			mutate(&cfg)
			if _, err := shell.NewSession(cfg); !errors.Is(err, shell.ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestRunRejectsEmptyScript(t *testing.T) {
	session, err := shell.NewSession(baseConfig(t))
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	if _, err := session.Run(context.Background(), "   \n\t "); !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
	if session.Calls() != 0 {
		t.Fatalf("expected the rejected call not to consume budget, got %d", session.Calls())
	}
}
