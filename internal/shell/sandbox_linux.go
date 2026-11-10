//go:build linux

package shell

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SandboxConfig describes the bubblewrap filesystem/namespace boundary.
//
// The sandbox is independent of the cgroup boundary: cgroup limits memory,
// processes and CPU, while bubblewrap gives the shell a private mount, PID,
// IPC, UTS, user and (optionally) network namespace with a read-only root.
type SandboxConfig struct {
	Enabled bool
	// Bubblewrap is the absolute path of the bubblewrap binary.
	Bubblewrap string
	// AllowNetwork keeps the host network namespace when true. The default
	// gives the shell a network namespace containing only loopback.
	AllowNetwork bool
	// WritablePaths are bind-mounted read-write over the read-only root. They
	// must be absolute, existing directories and must not be "/".
	WritablePaths []string
}

func (c SandboxConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.Bubblewrap) == "" {
		return fmt.Errorf("%w: bubblewrap path is required", ErrInvalidConfig)
	}
	if !filepath.IsAbs(c.Bubblewrap) {
		return fmt.Errorf("%w: bubblewrap path must be absolute", ErrInvalidConfig)
	}
	info, err := os.Stat(c.Bubblewrap)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return fmt.Errorf("%w: bubblewrap %q is not an executable file", ErrInvalidConfig, c.Bubblewrap)
	}
	seen := make(map[string]bool, len(c.WritablePaths))
	for _, raw := range c.WritablePaths {
		if !filepath.IsAbs(raw) {
			return fmt.Errorf("%w: sandbox writable path %q must be absolute", ErrInvalidConfig, raw)
		}
		cleaned := filepath.Clean(raw)
		if cleaned == "/" {
			return fmt.Errorf("%w: the sandbox root must not be writable", ErrInvalidConfig)
		}
		if seen[cleaned] {
			continue
		}
		seen[cleaned] = true
		info, err := os.Stat(cleaned)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("%w: sandbox writable path %q is not a directory", ErrInvalidConfig, cleaned)
		}
	}
	return nil
}

// ValidateSandbox checks the binary and actually creates a throwaway sandbox.
// A host that cannot create the requested namespaces must fail startup rather
// than fall back to an unsandboxed shell.
func ValidateSandbox(cfg SandboxConfig) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, cfg.Bubblewrap,
		"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL",
		"--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp",
		"--", "/bin/true",
	)
	probe.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp"}
	// If bubblewrap leaves a namespace child holding the output pipe, Wait
	// must not block the whole service startup forever.
	probe.WaitDelay = 2 * time.Second
	output, err := probe.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: bubblewrap probe failed: %v (%s)", ErrInvalidConfig, err, abbreviatedOutput(output))
	}
	return nil
}

// sandboxArgs wraps one already-built shell command. The command may be Bash
// directly or prlimit -> Bash; either way the namespace boundary is outside it.
func sandboxArgs(cfg Config, executable string, args []string) ([]string, error) {
	sandbox := cfg.Sandbox
	out := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-all",
		"--cap-drop", "ALL",
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
	}
	if sandbox.AllowNetwork {
		out = append(out, "--share-net")
	}

	writable := make(map[string]bool)
	for _, path := range sandbox.WritablePaths {
		writable[filepath.Clean(path)] = true
	}
	if strings.TrimSpace(cfg.ScratchDir) != "" {
		writable[filepath.Clean(cfg.ScratchDir)] = true
	}
	for path := range writable {
		if path == "/" {
			return nil, fmt.Errorf("%w: the sandbox root must not be writable", ErrInvalidConfig)
		}
		out = append(out, "--bind", path, path)
	}

	out = append(out, "--chdir", cfg.WorkDir, "--", executable)
	out = append(out, args...)
	return out, nil
}

func abbreviatedOutput(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if len(text) > 512 {
		text = text[:512] + "..."
	}
	return text
}
