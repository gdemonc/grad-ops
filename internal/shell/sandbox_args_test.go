//go:build linux

package shell

import (
	"errors"
	"strings"
	"testing"
)

func TestSandboxArgsApplyTheFilesystemAndNamespaceBoundary(t *testing.T) {
	cfg := Config{
		WorkDir: "/",
		Sandbox: SandboxConfig{
			Enabled:       true,
			Bubblewrap:    "/usr/bin/bwrap",
			WritablePaths: []string{"/var/log", "/var/log"},
		},
		ScratchDir: "/var/lib/gradops",
	}
	args, err := sandboxArgs(cfg, "/bin/bash", []string{"--noprofile", "--norc", "-c", "true"})
	if err != nil {
		t.Fatalf("sandbox args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--unshare-all",
		"--ro-bind / /",
		"--dev /dev",
		"--proc /proc",
		"--tmpfs /tmp",
		"--bind /var/log /var/log",
		"--bind /var/lib/gradops /var/lib/gradops",
		"--chdir / -- /bin/bash",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args do not contain %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "--share-net") {
		t.Fatal("network must be isolated by default")
	}
	if strings.Count(joined, "--bind /var/log /var/log") != 1 {
		t.Fatalf("writable path was not deduplicated: %s", joined)
	}
}

func TestSandboxArgsCanShareTheNetwork(t *testing.T) {
	cfg := Config{
		WorkDir: "/",
		Sandbox: SandboxConfig{
			Enabled:      true,
			Bubblewrap:   "/usr/bin/bwrap",
			AllowNetwork: true,
		},
	}
	args, err := sandboxArgs(cfg, "/bin/bash", []string{"-c", "true"})
	if err != nil {
		t.Fatalf("sandbox args: %v", err)
	}
	if !strings.Contains(strings.Join(args, " "), "--share-net") {
		t.Fatalf("expected --share-net: %v", args)
	}
}

func TestSandboxArgsRejectAWritableRoot(t *testing.T) {
	cfg := Config{
		WorkDir: "/",
		Sandbox: SandboxConfig{
			Enabled:       true,
			Bubblewrap:    "/usr/bin/bwrap",
			WritablePaths: []string{"/"},
		},
	}
	if _, err := sandboxArgs(cfg, "/bin/bash", []string{"-c", "true"}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestValidateSandboxFailsClosed(t *testing.T) {
	cases := map[string]SandboxConfig{
		"missing binary": {Enabled: true, Bubblewrap: "/no/such/bwrap"},
		"not executable": {Enabled: true, Bubblewrap: "/etc/hosts"},
		"writable root":  {Enabled: true, Bubblewrap: "/usr/bin/bwrap", WritablePaths: []string{"/"}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSandbox(cfg); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
		})
	}
}
