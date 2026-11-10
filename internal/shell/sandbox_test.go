//go:build linux

package shell_test

import (
	"context"
	"strings"
	"testing"

	"github.com/gdemonc/grad-ops/internal/shell"
)

func TestBubblewrapSessionHasNoHostNetworkOrWritableRoot(t *testing.T) {
	cfg := baseConfig(t)
	cfg.WorkDir = "/"
	cfg.Env = map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/tmp"}
	cfg.Sandbox = shell.SandboxConfig{
		Enabled:    true,
		Bubblewrap: "/usr/bin/bwrap",
	}
	if err := shell.ValidateSandbox(cfg.Sandbox); err != nil {
		t.Skipf("bubblewrap is not usable here: %v", err)
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	result, err := session.Run(context.Background(),
		`test ! -w /etc && echo root-readonly; test "$(ls /sys/class/net | wc -l)" -eq 1 && echo net-isolated`)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("sandboxed shell failed: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "root-readonly") || !strings.Contains(result.Stdout, "net-isolated") {
		t.Fatalf("sandbox boundary not visible: stdout=%q stderr=%q", result.Stdout, result.Stderr)
	}
}
