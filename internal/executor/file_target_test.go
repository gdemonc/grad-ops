package executor_test

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/fileops"
	"github.com/gdemonc/grad-ops/internal/policy"
)

func newFileHarness(t *testing.T) (*harness, string) {
	t.Helper()
	bash := requireBash(t)
	workdir := t.TempDir()
	root := t.TempDir()
	db := testDB(t)
	engine, err := policy.New(policy.Config{Version: "test-policy", AllowedRoots: []string{root}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	approvals, err := approval.New(db, 5*time.Minute)
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	service, err := executor.New(db, engine, approvals, evidence.New(), executor.Config{
		Bash:              bash,
		BaseEnv:           map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		RequireRunAsMatch: false,
		ExecutionTimeout:  5 * time.Second,
	}, executor.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	return &harness{db: db, service: service, approvals: approvals}, root
}

func buildFileContract(t *testing.T, root, relative, data string) (contract.Contract, string) {
	t.Helper()
	target, err := fileops.Bind(root, relative)
	if err != nil {
		t.Fatalf("bind target: %v", err)
	}
	now := time.Now().UTC()
	c := contract.Contract{
		Tool:       "fileops",
		Kind:       string(policy.KindModifyFile),
		Script:     "fileops append",
		WorkDir:    "/",
		Env:        map[string]string{},
		Paths:      []string{filepath.Join(root, relative)},
		RunAs:      currentUsername(t),
		Reversible: true,
		Runtime:    contract.Runtime{TimeoutMS: 5000, MaxOutputBytes: 4096, MaxCalls: 1},
		Postcondition: contract.Postcondition{
			Command: "printf ok",
		},
		Risk:          "R1",
		PolicyVersion: "test-policy",
		ApprovalLevel: "human",
		Nonce:         "0123456789abcdef0123456789abcdef",
		CreatedAt:     domain.FormatTime(now),
		ExpiresAt:     domain.FormatTime(now.Add(15 * time.Minute)),
		FileTarget: &contract.FileTarget{
			Root:       target.Root,
			RootDevice: target.RootIdentity.Dev,
			RootInode:  target.RootIdentity.Ino,
			Relative:   target.Relative,
			Operation:  "append",
			Data:       data,
			Device:     target.Identity.Dev,
			Inode:      target.Identity.Ino,
		},
	}
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return c, digest
}

func TestFileTargetAppendsThroughTheBoundFD(t *testing.T) {
	h, root := newFileHarness(t)
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("start\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, digest := buildFileContract(t, root, "app.log", "next\n")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID, ApprovalToken: token, Actor: "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusSucceeded {
		t.Fatalf("expected success, got %s: %s", outcome.Status, outcome.Stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "start\nnext\n" {
		t.Fatalf("unexpected file content: %q", content)
	}

	var tool, command string
	if err := h.db.QueryRow(`SELECT tool, command FROM evidence WHERE task_id = ?`, taskID).Scan(&tool, &command); err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	if tool != "fileops" || command == "" {
		t.Fatalf("unexpected evidence: tool=%q command=%q", tool, command)
	}
}

func TestFileTargetRejectsAnIdentityChange(t *testing.T) {
	h, root := newFileHarness(t)
	path := filepath.Join(root, "app.log")
	oldPath := filepath.Join(root, "old.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, digest := buildFileContract(t, root, "app.log", "must-not-append\n")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	if err := os.Rename(path, oldPath); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement\n"), 0o600); err != nil {
		t.Fatalf("replace: %v", err)
	}

	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID, ApprovalToken: token, Actor: "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusFailed {
		t.Fatalf("expected FAILED, got %s", outcome.Status)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read replacement: %v", err)
	}
	if string(content) != "replacement\n" {
		t.Fatalf("replacement was modified: %q", content)
	}

	var status, stage string
	if err := h.db.QueryRow(`SELECT status, failure_stage FROM tasks WHERE id = ?`, taskID).Scan(&status, &stage); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusFailed) || stage != "execution" {
		t.Fatalf("unexpected task state: %s/%s", status, stage)
	}
}

func currentUsername(t *testing.T) string {
	t.Helper()
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	return current.Username
}

func TestFileTargetRejectsADifferentRunAs(t *testing.T) {
	h, root := newFileHarness(t)
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, _ := buildFileContract(t, root, "app.log", "must-not-append\n")
	c.RunAs = "not-the-current-user"
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID, ApprovalToken: token, Actor: "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusFailed {
		t.Fatalf("expected FAILED, got %s", outcome.Status)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "original\n" {
		t.Fatalf("file was modified: %q", content)
	}
}

func TestFileTargetRejectsANonRegularReplacementAsFailed(t *testing.T) {
	h, root := newFileHarness(t)
	path := filepath.Join(root, "app.log")
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	c, digest := buildFileContract(t, root, "app.log", "must-not-append\n")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID, ApprovalToken: token, Actor: "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusFailed {
		t.Fatalf("expected FAILED for a non-regular target, got %s", outcome.Status)
	}
}
