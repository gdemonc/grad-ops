//go:build linux

package shell

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// CgroupConfig describes the cgroup v2 boundary for a shell process.
//
// When Enabled is set, every shell is started directly in a fresh child
// cgroup. The service must control a delegated cgroup subtree (systemd
// Delegate=yes, or an equivalent delegation); if it does not, startup fails
// instead of silently running without the boundary.
type CgroupConfig struct {
	Enabled bool
	// Root is the delegated cgroup directory. Empty discovers the service's
	// own cgroup from /proc/self/cgroup under /sys/fs/cgroup.
	Root string
	// MemoryMaxBytes and PidsMax are written to memory.max and pids.max.
	// Zero leaves that controller unlimited.
	MemoryMaxBytes int64
	PidsMax        int64
	// CPUQuotaMicros and CPUPeriodMicros are written to cpu.max as
	// "<quota> <period>". A zero quota leaves CPU unthrottled.
	CPUQuotaMicros  int64
	CPUPeriodMicros int64
}

func (c CgroupConfig) validate() error {
	switch {
	case c.MemoryMaxBytes < 0:
		return fmt.Errorf("%w: cgroup memory limit must not be negative", ErrInvalidConfig)
	case c.PidsMax < 0:
		return fmt.Errorf("%w: cgroup process limit must not be negative", ErrInvalidConfig)
	case c.CPUQuotaMicros < 0:
		return fmt.Errorf("%w: cgroup CPU quota must not be negative", ErrInvalidConfig)
	case c.CPUPeriodMicros < 0:
		return fmt.Errorf("%w: cgroup CPU period must not be negative", ErrInvalidConfig)
	case c.CPUQuotaMicros > 0 && c.CPUPeriodMicros <= 0:
		return fmt.Errorf("%w: cgroup CPU period must be positive when a quota is set", ErrInvalidConfig)
	case c.Root != "" && !filepath.IsAbs(c.Root):
		return fmt.Errorf("%w: cgroup root must be an absolute path", ErrInvalidConfig)
	}
	return nil
}

// ValidateCgroup checks configuration and verifies that a delegated cgroup v2
// subtree can actually be created. It is called at startup and fails closed:
// asking for cgroup isolation on a host that cannot provide it must not start.
func ValidateCgroup(cfg CgroupConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	guard, err := newCgroupGuard(cfg)
	if err != nil {
		return err
	}
	return guard.close()
}

type cgroupGuard struct {
	dir string
	fd  *os.File
}

// newCgroupGuard creates one child cgroup and writes its limits. A disabled
// configuration returns an empty guard whose methods are no-ops.
func newCgroupGuard(cfg CgroupConfig) (*cgroupGuard, error) {
	if !cfg.Enabled {
		return &cgroupGuard{}, nil
	}

	base, err := cgroupBase(cfg)
	if err != nil {
		return nil, err
	}
	if err := ensureCgroupControllers(base, cfg); err != nil {
		return nil, err
	}
	name, err := cgroupName()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(base, name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create cgroup %q: %w", dir, err)
	}

	cleanup := func() {
		_ = os.Remove(dir)
	}
	if err := writeCgroupFile(dir, "memory.max", cfg.MemoryMaxBytes); err != nil {
		cleanup()
		return nil, err
	}
	if err := writeCgroupFile(dir, "pids.max", cfg.PidsMax); err != nil {
		cleanup()
		return nil, err
	}
	if cfg.CPUQuotaMicros > 0 {
		value := fmt.Sprintf("%d %d", cfg.CPUQuotaMicros, cfg.CPUPeriodMicros)
		if err := os.WriteFile(filepath.Join(dir, "cpu.max"), []byte(value), 0o644); err != nil {
			cleanup()
			return nil, fmt.Errorf("write %s/cpu.max: %w", dir, err)
		}
	}

	fd, err := os.Open(dir)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open cgroup %q: %w", dir, err)
	}
	return &cgroupGuard{dir: dir, fd: fd}, nil
}

// attach makes the child enter the cgroup at clone time with CLONE_INTO_CGROUP.
// It must be called after configureProcess has installed SysProcAttr.
func (g *cgroupGuard) attach(command *exec.Cmd) error {
	if g == nil || g.fd == nil {
		return nil
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.UseCgroupFD = true
	command.SysProcAttr.CgroupFD = int(g.fd.Fd())
	return nil
}

func (g *cgroupGuard) kill() {
	if g == nil || g.dir == "" {
		return
	}
	// cgroup.kill kills every process in the cgroup, including descendants
	// that changed process group. It is best-effort; process-group signal is
	// still sent by the caller.
	_ = os.WriteFile(filepath.Join(g.dir, "cgroup.kill"), []byte("1"), 0o644)
}

func (g *cgroupGuard) close() error {
	if g == nil || g.dir == "" {
		return nil
	}
	g.kill()
	if g.fd != nil {
		_ = g.fd.Close()
		g.fd = nil
	}

	var err error
	for attempt := 0; attempt < 100; attempt++ {
		err = os.Remove(g.dir)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			g.dir = ""
			return nil
		}
		if !errors.Is(err, syscall.EBUSY) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("remove cgroup %q: %w", g.dir, err)
}

func writeCgroupFile(dir, name string, value int64) error {
	if value <= 0 {
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.FormatInt(value, 10)), 0o644); err != nil {
		return fmt.Errorf("write %s/%s: %w", dir, name, err)
	}
	return nil
}

// ensureCgroupControllers enables the controllers the child cgroups need.
//
// A delegated systemd service should use DelegateSubgroup= so its own PID is
// outside the delegated base cgroup; enabling controllers on a cgroup that
// still contains processes fails with EBUSY, and that is a startup error rather
// than a silent downgrade.
func ensureCgroupControllers(base string, cfg CgroupConfig) error {
	wanted := make([]string, 0, 3)
	if cfg.MemoryMaxBytes > 0 {
		wanted = append(wanted, "memory")
	}
	if cfg.PidsMax > 0 {
		wanted = append(wanted, "pids")
	}
	if cfg.CPUQuotaMicros > 0 {
		wanted = append(wanted, "cpu")
	}
	if len(wanted) == 0 {
		return nil
	}

	raw, err := os.ReadFile(filepath.Join(base, "cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read cgroup controllers in %q: %w", base, err)
	}
	available := make(map[string]bool)
	for _, name := range strings.Fields(string(raw)) {
		available[name] = true
	}
	for _, name := range wanted {
		if !available[name] {
			return fmt.Errorf("%w: cgroup controller %q is not delegated at %q", ErrInvalidConfig, name, base)
		}
	}
	value := "+" + strings.Join(wanted, " +")
	if err := os.WriteFile(filepath.Join(base, "cgroup.subtree_control"), []byte(value), 0o644); err != nil {
		return fmt.Errorf("enable cgroup controllers %q at %q (the service PID must be outside the delegated base cgroup): %w", value, base, err)
	}
	return nil
}

func cgroupName() (string, error) {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate cgroup name: %w", err)
	}
	return fmt.Sprintf("gradops-%d-%s", os.Getpid(), hex.EncodeToString(raw)), nil
}

func cgroupBase(cfg CgroupConfig) (string, error) {
	root := cfg.Root
	if root == "" {
		data, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			return "", fmt.Errorf("read /proc/self/cgroup: %w", err)
		}
		path, err := cgroupV2Path(string(data))
		if err != nil {
			return "", err
		}
		root = filepath.Join("/sys/fs/cgroup", path)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: cgroup root must be absolute", ErrInvalidConfig)
	}
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		return "", fmt.Errorf("%w: cgroup root %q is not a cgroup v2 hierarchy: %v", ErrInvalidConfig, root, err)
	}
	return root, nil
}

func cgroupV2Path(proc string) (string, error) {
	for _, line := range strings.Split(proc, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 || fields[0] != "0" || fields[1] != "" {
			continue
		}
		if !strings.HasPrefix(fields[2], "/") {
			return "", fmt.Errorf("unusable cgroup v2 path %q", fields[2])
		}
		path := strings.TrimSuffix(strings.TrimPrefix(fields[2], "/"), "/")
		// The unit uses DelegateSubgroup=supervisor so the service's own PID is
		// outside the delegated base cgroup; child cgroups belong to the base.
		if path == "supervisor" {
			return "", nil
		}
		path = strings.TrimSuffix(path, "/supervisor")
		return path, nil
	}
	return "", fmt.Errorf("no cgroup v2 entry in /proc/self/cgroup")
}
