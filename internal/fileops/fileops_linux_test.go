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

func TestTargetAppendUsesTheBoundIdentity(t *testing.T) {
	root, dir := testRoot(t)
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("start\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	target, err := fileops.Bind(root.Path(), "app.log")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := target.Append([]byte("next\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "start\nnext\n" {
		t.Fatalf("unexpected content: %q", content)
	}
}

func TestTargetAppendRejectsAReplacedPath(t *testing.T) {
	root, dir := testRoot(t)
	path := filepath.Join(dir, "app.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	target, err := fileops.Bind(root.Path(), "app.log")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	if err := os.Rename(path, filepath.Join(dir, "old.log")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if err := target.Append([]byte("must-not-append\n")); !errors.Is(err, fileops.ErrIdentityMismatch) {
		t.Fatalf("expected ErrIdentityMismatch, got %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(content) != "replacement\n" {
		t.Fatalf("replacement was modified: %q", content)
	}
}

func TestTargetAppendRejectsASymlinkSwap(t *testing.T) {
	root, dir := testRoot(t)
	path := filepath.Join(dir, "app.log")
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	target, err := fileops.Bind(root.Path(), "app.log")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := target.Append([]byte("must-not-append\n")); !errors.Is(err, fileops.ErrUnsafePath) {
		t.Fatalf("expected ErrUnsafePath, got %v", err)
	}
	content, err := os.ReadFile(outside)
	if err != nil {
		t.Fatalf("read outside: %v", err)
	}
	if string(content) != "outside\n" {
		t.Fatalf("outside target was modified: %q", content)
	}
}

func TestTargetAppendRejectsAReplacedRoot(t *testing.T) {
	root, dir := testRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "app.log"), []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	target, err := fileops.Bind(root.Path(), "app.log")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	oldRoot := dir + "-old"
	if err := os.Rename(dir, oldRoot); err != nil {
		t.Fatalf("rename root: %v", err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create replacement root: %v", err)
	}
	if err := os.Link(filepath.Join(oldRoot, "app.log"), filepath.Join(dir, "app.log")); err != nil {
		t.Fatalf("link target: %v", err)
	}
	if err := target.Append([]byte("must-not-append\n")); !errors.Is(err, fileops.ErrIdentityMismatch) {
		t.Fatalf("expected root identity mismatch, got %v", err)
	}
	content, err := os.ReadFile(filepath.Join(oldRoot, "app.log"))
	if err != nil {
		t.Fatalf("read old root: %v", err)
	}
	if string(content) != "original\n" {
		t.Fatalf("old target was modified: %q", content)
	}
}
