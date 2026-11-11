package investigation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gdemonc/grad-ops/internal/policy"
)

func TestBindFileActionUsesTrustedRootAndIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.log"), []byte("start\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	service := &Service{cfg: Config{FileRoots: []string{root}}}
	target, script, paths, err := service.bindFileAction(&Action{
		Kind: policy.KindModifyFile,
		File: &FileAction{Root: root, Relative: "app.log", Data: "next\n"},
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if target == nil || target.Device == 0 || target.Inode == 0 {
		t.Fatalf("missing target identity: %+v", target)
	}
	if target.Root != filepath.Clean(root) || target.Relative != "app.log" || target.Operation != "append" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if script == "" || len(paths) != 1 || paths[0] != filepath.Join(root, "app.log") {
		t.Fatalf("unexpected script/paths: %q %v", script, paths)
	}
}

func TestBindFileActionRejectsUntrustedRoot(t *testing.T) {
	service := &Service{cfg: Config{FileRoots: []string{t.TempDir()}}}
	_, _, _, err := service.bindFileAction(&Action{
		Kind: policy.KindModifyFile,
		File: &FileAction{Root: t.TempDir(), Relative: "app.log", Data: "x"},
	})
	if err == nil {
		t.Fatal("expected an untrusted root to be rejected")
	}
}
