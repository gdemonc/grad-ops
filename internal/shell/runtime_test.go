//go:build linux

package shell_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/shell"
)

// requirePrlimit locates util-linux prlimit or skips. The limit tests must not
// pass on a host where the tool is missing; that would be the same silent
// downgrade the runtime code refuses to make.
func requirePrlimit(t *testing.T) string {
	t.Helper()
	found, err := exec.LookPath("prlimit")
	if err != nil {
		t.Skip("prlimit is not installed; resource-limit behaviour is not verified here")
	}
	absolute, err := filepath.Abs(found)
	if err != nil {
		t.Fatalf("resolve prlimit: %v", err)
	}
	return absolute
}

func TestResourceLimitsAreApplied(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Prlimit = requirePrlimit(t)
	cfg.Limits = shell.Limits{
		CPU:               5 * time.Second,
		AddressSpaceBytes: 256 << 20,
		MaxProcesses:      64,
		MaxFileSizeBytes:  1 << 20,
		MaxOpenFiles:      128,
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	result, err := session.Run(context.Background(),
		`printf '%s %s %s %s %s\n' "$(ulimit -v)" "$(ulimit -t)" "$(ulimit -u)" "$(ulimit -f)" "$(ulimit -n)"`,
	)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("script failed: exit=%d stderr=%q", result.ExitCode, result.Stderr)
	}

	const want = "262144 5 64 1024 128"
	if got := strings.TrimSpace(result.Stdout); got != want {
		t.Fatalf("limits reported by the shell = %q, want %q", got, want)
	}
}

func TestLimitsRequireAnExecutablePrlimit(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Limits = shell.Limits{CPU: time.Second}
	cfg.Prlimit = filepath.Join(cfg.WorkDir, "missing-prlimit")

	_, err := shell.NewSession(cfg)
	if !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for a missing prlimit, got %v", err)
	}
}

func TestLimitsRejectRelativePrlimit(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Limits = shell.Limits{CPU: time.Second}
	cfg.Prlimit = "prlimit"

	_, err := shell.NewSession(cfg)
	if !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for a relative prlimit, got %v", err)
	}
}

func TestLimitsRejectNegativeValues(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Limits = shell.Limits{AddressSpaceBytes: -1}

	// No prlimit is needed to reject the configuration itself.
	_, err := shell.NewSession(cfg)
	if !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for a negative limit, got %v", err)
	}
}

func TestRunAsCurrentUserIsAccepted(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve the current user: %v", err)
	}

	cfg := baseConfig(t)
	cfg.RunAs = current.Username

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session for the current user: %v", err)
	}
	result, err := session.Run(context.Background(), "id -u")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(result.Stdout) != current.Uid {
		t.Fatalf("shell ran as uid %q, want %q", strings.TrimSpace(result.Stdout), current.Uid)
	}
}

func TestRunAsUnknownIdentityIsRejected(t *testing.T) {
	cfg := baseConfig(t)
	cfg.RunAs = "gradops-user-that-does-not-exist"

	_, err := shell.NewSession(cfg)
	if !errors.Is(err, shell.ErrIdentityUnavailable) {
		t.Fatalf("expected ErrIdentityUnavailable, got %v", err)
	}
}

func TestRunAsAnotherUserFailsClosedWithoutRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; this test covers the non-root refusal")
	}

	other, err := user.Lookup("nobody")
	if err != nil {
		t.Skipf("no nobody account: %v", err)
	}
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve the current user: %v", err)
	}
	if other.Uid == current.Uid {
		t.Skip("the test user already is nobody")
	}

	cfg := baseConfig(t)
	cfg.RunAs = other.Username

	_, err = shell.NewSession(cfg)
	if !errors.Is(err, shell.ErrIdentityUnavailable) {
		t.Fatalf("expected ErrIdentityUnavailable, got %v", err)
	}
}

func TestValidateRuntimeChecksIdentityAndToolWithoutStartingAShell(t *testing.T) {
	if err := shell.ValidateRuntime("gradops-user-that-does-not-exist", shell.Limits{}, ""); !errors.Is(err, shell.ErrIdentityUnavailable) {
		t.Fatalf("expected ErrIdentityUnavailable, got %v", err)
	}
	if err := shell.ValidateRuntime("", shell.Limits{CPU: time.Second}, "/no/such/prlimit"); !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for a missing prlimit, got %v", err)
	}
}
