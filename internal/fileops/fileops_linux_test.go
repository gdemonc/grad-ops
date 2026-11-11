//go:build linux

package fileops_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gdemonc/grad-ops/internal/fileops"
)

func testRoot(t *testing.T) (*fileops.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := fileops.OpenRoot(dir)
	if err != nil {
		t.Fatalf("open root: %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root, dir
}

func TestOpenRootRejectsRelativePath(t *testing.T) {
	if _, err := fileops.OpenRoot("var/log"); !errors.Is(err, fileops.ErrInvalidRoot) {
		t.Fatalf("expected ErrInvalidRoot, got %v", err)
	}
}

func TestOpenRejectsAbsoluteAndParentPaths(t *testing.T) {
	root, _ := testRoot(t)
	for _, target := range []string{"/etc/passwd", "../outside", "a/../../outside", "a/../inside", "."} {
		t.Run(target, func(t *testing.T) {
			if _, err := root.Read(target); !errors.Is(err, fileops.ErrInvalidPath) {
				t.Fatalf("expected ErrInvalidPath for %q, got %v", target, err)
			}
		})
	}
}

func TestOpenRejectsSymlinksEvenInsideTheRoot(t *testing.T) {
	root, dir := testRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "inside-link")); err != nil {
		t.Fatalf("symlink inside: %v", err)
	}
	if _, err := root.Read("inside-link"); !errors.Is(err, fileops.ErrUnsafePath) {
		t.Fatalf("expected inside symlink to be rejected, got %v", err)
	}
}

func TestOpenRejectsSymlinkEscape(t *testing.T) {
	root, dir := testRoot(t)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Fatalf("symlink escape: %v", err)
	}
	if _, err := root.Read("escape"); !errors.Is(err, fileops.ErrUnsafePath) {
		t.Fatalf("expected symlink escape to be rejected, got %v", err)
	}
}

func TestReadWriteAndIdentityStayOnTheOpenedFD(t *testing.T) {
	root, dir := testRoot(t)
	file, err := root.Write("target", os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := file.WriteString("original"); err != nil {
		t.Fatalf("write: %v", err)
	}
	identity, err := fileops.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	// Replace the string path after the fd is open. The fd must continue to
	// refer to the original inode, not whatever now lives at that path.
	if err := os.Rename(filepath.Join(dir, "target"), filepath.Join(dir, "old")); err != nil {
		t.Fatalf("rename old: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if _, err := file.WriteString("-updated"); err != nil {
		t.Fatalf("write via fd: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	oldContent, err := os.ReadFile(filepath.Join(dir, "old"))
	if err != nil {
		t.Fatalf("read old: %v", err)
	}
	newContent, err := os.ReadFile(filepath.Join(dir, "target"))
	if err != nil {
		t.Fatalf("read new: %v", err)
	}
	if string(oldContent) != "original-updated" {
		t.Fatalf("fd wrote the wrong inode: %q", oldContent)
	}
	if string(newContent) != "replacement" {
		t.Fatalf("replacement was modified: %q", newContent)
	}

	again, err := root.Read("old")
	if err != nil {
		t.Fatalf("reopen old: %v", err)
	}
	defer again.Close()
	againIdentity, err := fileops.Stat(again)
	if err != nil {
		t.Fatalf("stat reopened: %v", err)
	}
	if againIdentity.Dev != identity.Dev || againIdentity.Ino != identity.Ino {
		t.Fatalf("identity changed: %+v vs %+v", identity, againIdentity)
	}
}
