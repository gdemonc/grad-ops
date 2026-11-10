//go:build linux

package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupStaleCgroupsRemovesOnlyGradopsChildren(t *testing.T) {
	base := t.TempDir()
	stale := filepath.Join(base, "gradops-123-dead")
	other := filepath.Join(base, "keep-me")
	if err := os.Mkdir(stale, 0o755); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	// A real cgroup.kill is a special file. Make the directory read-only so
	// the test exercises the kill failure and still removes the empty dir.
	if err := os.Chmod(stale, 0o555); err != nil {
		t.Fatalf("chmod stale: %v", err)
	}
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}

	if err := cleanupStaleCgroups(base); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale cgroup was not removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("unrelated directory was removed: %v", err)
	}
}
