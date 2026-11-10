//go:build linux

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

func TestValidateCgroupDisabledIsANoOp(t *testing.T) {
	if err := shell.ValidateCgroup(shell.CgroupConfig{}); err != nil {
		t.Fatalf("disabled cgroup: %v", err)
	}
}

func TestValidateCgroupRejectsInvalidLimits(t *testing.T) {
	cases := map[string]shell.CgroupConfig{
		"negative memory": {Enabled: true, MemoryMaxBytes: -1},
		"negative pids":   {Enabled: true, PidsMax: -1},
		"quota no period": {Enabled: true, CPUQuotaMicros: 1},
		"relative root":   {Enabled: true, Root: "cgroup"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := shell.ValidateCgroup(cfg); !errors.Is(err, shell.ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestValidateCgroupFailsOutsideACgroupHierarchy(t *testing.T) {
	err := shell.ValidateCgroup(shell.CgroupConfig{Enabled: true, Root: t.TempDir()})
	if !errors.Is(err, shell.ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig for a non-cgroup directory, got %v", err)
	}
}

func testCgroupConfig() shell.CgroupConfig {
	cfg := shell.CgroupConfig{
		Enabled:        true,
		MemoryMaxBytes: 64 << 20,
		PidsMax:        32,
	}
	if root := strings.TrimSpace(os.Getenv("GRADOPS_TEST_CGROUP_ROOT")); root != "" {
		cfg.Root = root
	}
	return cfg
}

func TestSessionRunsInADedicatedCgroup(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Cgroup = testCgroupConfig()
	if err := shell.ValidateCgroup(cfg.Cgroup); err != nil {
		t.Skipf("no delegated writable cgroup v2 subtree here: %v", err)
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := session.Run(context.Background(),
		`path=$(cut -d: -f3 /proc/self/cgroup); printf 'path=%s\n' "$path"; cat "/sys/fs/cgroup$path/memory.max"; cat "/sys/fs/cgroup$path/pids.max"`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "gradops-") {
		t.Fatalf("shell did not report a gradops cgroup: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "67108864") || !strings.Contains(result.Stdout, "32") {
		t.Fatalf("cgroup limits were not applied: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}

func TestCgroupTimeoutKillsTheGroup(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Timeout = 150 * time.Millisecond
	cfg.Cgroup = testCgroupConfig()
	if err := shell.ValidateCgroup(cfg.Cgroup); err != nil {
		t.Skipf("no delegated writable cgroup v2 subtree here: %v", err)
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := session.Run(context.Background(), "sleep 5 & wait")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("expected a timeout, got %+v", result)
	}
}
