// Package e2e exercises the whole control loop through its real components.
//
// Nothing here is faked except the planner: the task goes through the durable
// queue, the worker claims it, the investigation records evidence and creates a
// proposal, the approval is consumed, the executor runs the action, and an
// independent verification seals the task.
package e2e_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
	"github.com/gdemonc/grad-ops/internal/verify"
	"github.com/gdemonc/grad-ops/internal/worker"
)

func requireBash(t *testing.T) string {
	t.Helper()

	candidate := os.Getenv("GRADOPS_TEST_BASH")
	if candidate == "" {
		found, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("no bash available; set GRADOPS_TEST_BASH")
		}
		candidate = found
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("resolve bash: %v", err)
	}
	probe := exec.Command(absolute, "--noprofile", "--norc", "-c", "printf ok")
	if output, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Skipf("bash at %s is not usable: %v (%s)", absolute, err, output)
	}
	return absolute
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition was not met before the deadline")
}

func TestFullLoop(t *testing.T) {
	bash := requireBash(t)
	workdir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "gradops.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine, err := policy.New(policy.Config{
		Version:         "e2e-policy",
		AllowedRoots:    []string{"/var/log"},
		AutoApproveRefs: nil,
	})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}

	planner := investigation.ScriptedPlanner{
		Probes: []string{"printf 'service is inactive'"},
		Action: &investigation.Action{
			Kind:       policy.KindServiceControl,
			Script:     "printf 'restarted'",
			Paths:      []string{"/var/log/app.log"},
			RunAs:      "gradops-agent",
			Reversible: true,
			Postcondition: contract.Postcondition{
				Command:        "printf 'active'",
				StdoutContains: "active",
			},
			Rationale: "the probe reports the service is inactive",
		},
	}

	investigator, err := investigation.New(db, engine, planner, investigation.Config{
		Bash:           bash,
		BaseEnv:        map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		WorkDir:        workdir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		MaxCalls:       5,
	})
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}

	approvals, err := approval.New(db, 5*time.Minute)
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}

	exec, err := executor.New(db, engine, approvals, evidence.New(), executor.Config{
		Bash:    bash,
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
	}, executor.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("executor: %v", err)
	}

	verifier, err := verify.New(db, evidence.New(), verify.Config{
		Bash:    bash,
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		Timeout: 10 * time.Second,
	}, verify.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	tasks := task.NewService(db)
	created, err := tasks.Create(ctx, task.CreateInput{RequestText: "the app looks down", Source: "e2e"})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	// The worker claims the durable job and runs the investigation.
	wk, err := worker.New(db, worker.DefaultConfig("e2e-worker"), logger)
	if err != nil {
		t.Fatalf("worker: %v", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()
	go func() { _ = wk.Run(runCtx, investigator) }()

	waitFor(t, 20*time.Second, func() bool {
		var status string
		if err := db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, created.ID).Scan(&status); err != nil {
			return false
		}
		if status != string(domain.StatusAwaitingApproval) {
			return false
		}
		// Wait for the job to be marked done as well: stopping the worker at
		// the first sign of progress would race the completion audit event.
		var jobStatus string
		if err := db.QueryRow(
			`SELECT status FROM task_jobs WHERE task_id = ? ORDER BY id DESC LIMIT 1`,
			created.ID).Scan(&jobStatus); err != nil {
			return false
		}
		return jobStatus == string(domain.JobStatusDone)
	})
	stopRun()

	// The investigation must have left a proposal whose contract verifies.
	var (
		proposalID int64
		raw        string
		digest     string
	)
	if err := db.QueryRow(
		`SELECT id, contract, contract_digest FROM proposals WHERE task_id = ? ORDER BY id DESC LIMIT 1`,
		created.ID).Scan(&proposalID, &raw, &digest); err != nil {
		t.Fatalf("load proposal: %v", err)
	}

	var frozen contract.Contract
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if err := contract.Verify(frozen, digest); err != nil {
		t.Fatalf("proposal contract does not verify: %v", err)
	}
	if frozen.Risk != string(domain.RiskR2) {
		t.Fatalf("expected R2 for a service restart, got %s", frozen.Risk)
	}

	// A human approves the exact contract.
	grant, err := approvals.Grant(ctx, approval.Request{
		TaskID:         created.ID,
		ProposalID:     proposalID,
		ContractDigest: digest,
		Approver:       "alice",
		Comment:        "restart approved",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	outcome, err := exec.Execute(ctx, executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: grant.Token,
		Actor:         "e2e-worker",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s", outcome.Status)
	}

	verification, err := verifier.Verify(ctx, created.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !verification.Passed {
		t.Fatalf("expected the postcondition to hold, failures: %v", verification.Failures)
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, created.ID).Scan(&status); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusSealed) {
		t.Fatalf("expected the task to be SEALED, got %s", status)
	}

	// Every step must be on the audit chain, and the chain must still verify.
	events, err := audit.ListByTask(ctx, db, created.ID)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	joined := strings.Join(types, ",")
	for _, want := range []string{
		"task.created",
		"task.investigation.started",
		"investigation.proposed",
		"approval.granted",
		"approval.consumed",
		"execution.started",
		"execution.completed",
		"verification.passed",
		"job.completed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit chain is missing %q: %s", want, joined)
		}
	}

	verified, err := audit.Verify(ctx, db)
	if err != nil {
		t.Fatalf("verify audit: %v", err)
	}
	if !verified.OK {
		t.Fatal("audit chain did not verify")
	}

	var evidenceCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM evidence WHERE task_id = ?`, created.ID).Scan(&evidenceCount); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evidenceCount < 3 {
		t.Fatalf("expected probe, execution and verification evidence, got %d", evidenceCount)
	}
}
