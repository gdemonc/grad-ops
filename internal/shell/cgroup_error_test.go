//go:build linux

package shell

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestCgroupGuardReportsKillAndRemoveFailures(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing")
	guard := &cgroupGuard{dir: dir}

	if err := guard.kill(); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("expected ErrCleanupFailed from kill, got %v", err)
	}
	if err := guard.close(); !errors.Is(err, ErrCleanupFailed) {
		t.Fatalf("expected ErrCleanupFailed from close, got %v", err)
	}
}
