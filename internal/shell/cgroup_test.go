//go:build linux

package shell_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

func TestCgroupCPUQuotaIsApplied(t *testing.T) {
	rawQuota := strings.TrimSpace(os.Getenv("GRADOPS_TEST_CGROUP_CPU_QUOTA_MICROS"))
	if rawQuota == "" || rawQuota == "0" {
		t.Skip("set a non-zero GRADOPS_TEST_CGROUP_CPU_QUOTA_MICROS to exercise cpu.max")
	}
	rawPeriod := strings.TrimSpace(os.Getenv("GRADOPS_TEST_CGROUP_CPU_PERIOD_MICROS"))
	if rawPeriod == "" {
		rawPeriod = "100000"
	}
	var quota, period int64
	if _, err := fmt.Sscanf(rawQuota, "%d", &quota); err != nil || quota <= 0 {
		t.Fatalf("invalid quota %q", rawQuota)
	}
	if _, err := fmt.Sscanf(rawPeriod, "%d", &period); err != nil || period <= 0 {
		t.Fatalf("invalid period %q", rawPeriod)
	}

	cfg := baseConfig(t)
	cfg.Cgroup = testCgroupConfig()
	cfg.Cgroup.CPUQuotaMicros = quota
	cfg.Cgroup.CPUPeriodMicros = period
	if err := shell.ValidateCgroup(cfg.Cgroup); err != nil {
		t.Skipf("no delegated cgroup with cpu controller here: %v", err)
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := session.Run(context.Background(),
		`path=$(cut -d: -f3 /proc/self/cgroup); cat "/sys/fs/cgroup$path/cpu.max"`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	want := fmt.Sprintf("%d %d", quota, period)
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, want) {
		t.Fatalf("cpu.max = %q, want %q (stderr=%q)", result.Stdout, want, result.Stderr)
	}
}

func TestCgroupMemoryLimitIsEnforced(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Timeout = 3 * time.Second
	cfg.Cgroup = testCgroupConfig()
	cfg.Cgroup.MemoryMaxBytes = 16 << 20
	if err := shell.ValidateCgroup(cfg.Cgroup); err != nil {
		t.Skipf("no delegated writable cgroup v2 subtree here: %v", err)
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := session.Run(context.Background(),
		`path=$(cut -d: -f3 /proc/self/cgroup); echo 0 > "/sys/fs/cgroup$path/memory.swap.max" 2>/dev/null || true; x=$(head -c 67108864 /dev/zero | tr '\0' x)`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode == 0 && !result.TimedOut {
		t.Fatalf("a 64 MiB allocation succeeded under a 16 MiB cgroup: %+v", result)
	}
}

func TestCgroupTimeoutKillsAnEscapedProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid is not installed")
	}
	cfg := baseConfig(t)
	cfg.Timeout = 200 * time.Millisecond
	cfg.Cgroup = testCgroupConfig()
	if err := shell.ValidateCgroup(cfg.Cgroup); err != nil {
		t.Skipf("no delegated writable cgroup v2 subtree here: %v", err)
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	marker := filepath.Join(cfg.WorkDir, "escaped.pid")
	script := fmt.Sprintf(`setsid sh -c 'echo $$ > %q; exec sleep 5' & wait`, marker)
	result, err := session.Run(context.Background(), script)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !result.TimedOut {
		t.Fatalf("expected a timeout, got %+v", result)
	}

	rawPID, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read escaped pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid escaped pid %q: %v", rawPID, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("escaped process %d is still alive after timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
