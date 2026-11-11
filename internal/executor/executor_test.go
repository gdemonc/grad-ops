package executor_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
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
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
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

func testDB(t *testing.T) *store.DB {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gradops.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func buildContract(t *testing.T, script, risk string) (contract.Contract, string) {
	t.Helper()

	now := time.Now().UTC()
	c := contract.Contract{
		Tool:          "bash",
		Kind:          string(policy.KindServiceControl),
		Script:        script,
		WorkDir:       "/work",
		Env:           map[string]string{"PATH": os.Getenv("PATH")},
		Paths:         []string{"/var/log/app.log"},
		RunAs:         "gradops-agent",
		Reversible:    true,
		Runtime:       contract.Runtime{TimeoutMS: 5000, MaxOutputBytes: 4096, MaxCalls: 5},
		Postcondition: contract.Postcondition{Command: "printf ok"},
		Risk:          risk,
		PolicyVersion: "test-policy",
		ApprovalLevel: "human",
		Nonce:         "0123456789abcdef0123456789abcdef",
		CreatedAt:     domain.FormatTime(now),
		ExpiresAt:     domain.FormatTime(now.Add(15 * time.Minute)),
	}
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize contract: %v", err)
	}
	digest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest contract: %v", err)
	}
	return c, digest
}

func seed(t *testing.T, db *store.DB, c contract.Contract, digest string) (taskID, proposalID int64) {
	t.Helper()

	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("encode contract: %v", err)
	}

	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		now := domain.FormatTime(time.Now())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"restart the app", string(domain.StatusAwaitingApproval), now, now)
		if err != nil {
			return err
		}
		if taskID, err = result.LastInsertId(); err != nil {
			return err
		}

		result, err = tx.Exec(
			`INSERT INTO proposals
			    (task_id, tool, arguments, risk_level, policy_version, contract, contract_digest, status, created_at)
			 VALUES (?, ?, '{}', ?, ?, ?, ?, 'PENDING_APPROVAL', ?)`,
			taskID, c.Tool, c.Risk, c.PolicyVersion, string(raw), digest, now)
		if err != nil {
			return err
		}
		proposalID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return taskID, proposalID
}

type harness struct {
	db        *store.DB
	service   *executor.Service
	approvals *approval.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, false)
}

func newHarnessWith(t *testing.T, enforceRunAs bool) *harness {
	t.Helper()
	return newHarnessRuntime(t, enforceRunAs, "")
}

func newHarnessRuntime(t *testing.T, enforceRunAs bool, runAs string) *harness {
	t.Helper()
	return newHarnessConfig(t, enforceRunAs, runAs, 0)
}

func newHarnessConfig(t *testing.T, enforceRunAs bool, runAs string, executionTimeout time.Duration) *harness {
	t.Helper()

	bash := requireBash(t)
	workdir := t.TempDir()

	db := testDB(t)
	engine, err := policy.New(policy.Config{Version: "test-policy", AllowedRoots: []string{"/var/log"}})
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
		RequireRunAsMatch: enforceRunAs,
		RunAs:             runAs,
		ExecutionTimeout:  executionTimeout,
	}, executor.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("executor: %v", err)
	}

	return &harness{db: db, service: service, approvals: approvals}
}

func (h *harness) approve(t *testing.T, taskID, proposalID int64, digest string) string {
	t.Helper()

	grant, err := h.approvals.Grant(context.Background(), approval.Request{
		TaskID:         taskID,
		ProposalID:     proposalID,
		ContractDigest: digest,
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	return grant.Token
}

func taskStatus(t *testing.T, db *store.DB, taskID int64) (string, sql.NullString) {
	t.Helper()

	var status string
	var stage sql.NullString
	if err := db.QueryRow(`SELECT status, failure_stage FROM tasks WHERE id = ?`, taskID).Scan(&status, &stage); err != nil {
		t.Fatalf("read task: %v", err)
	}
	return status, stage
}

func TestExecuteApprovedAction(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "printf 'restarted'", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (%s)", outcome.Status, outcome.Stderr)
	}
	if outcome.Stdout != "restarted" {
		t.Fatalf("unexpected stdout %q", outcome.Stdout)
	}

	status, stage := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusVerifying) {
		t.Fatalf("expected the task to be VERIFYING, got %s", status)
	}
	if stage.Valid {
		t.Fatalf("expected no failure stage, got %q", stage.String)
	}

	var proposalStatus string
	if err := h.db.QueryRow(`SELECT status FROM proposals WHERE id = ?`, proposalID).Scan(&proposalStatus); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if proposalStatus != "EXECUTED" {
		t.Fatalf("expected the proposal to be EXECUTED, got %s", proposalStatus)
	}

	var evidenceCount int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM evidence WHERE task_id = ?`, taskID).Scan(&evidenceCount); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evidenceCount != 1 {
		t.Fatalf("expected one evidence record, got %d", evidenceCount)
	}

	verified, err := audit.Verify(context.Background(), h.db)
	if err != nil {
		t.Fatalf("verify audit: %v", err)
	}
	if !verified.OK {
		t.Fatal("audit chain did not verify")
	}
}

func TestRejectsTamperedContract(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "printf 'a'", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	tampered := c
	tampered.Script = "printf 'b'; rm -rf /var/log"
	raw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatalf("encode tampered contract: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE proposals SET contract = ? WHERE id = ?`, string(raw), proposalID); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	_, err = h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if !errors.Is(err, executor.ErrContractTampered) {
		t.Fatalf("expected ErrContractTampered, got %v", err)
	}

	var executions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records`).Scan(&executions); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if executions != 0 {
		t.Fatal("a tampered contract must not produce an execution record")
	}
	if status, _ := taskStatus(t, h.db, taskID); status != string(domain.StatusAwaitingApproval) {
		t.Fatalf("the task should be untouched, got %s", status)
	}
}

func TestRejectsRiskThatPolicyNoLongerAgreesWith(t *testing.T) {
	h := newHarness(t)
	// The contract claims R1 for a service restart, which policy classifies R2.
	c, digest := buildContract(t, "printf 'x'", "R1")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	_, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if !errors.Is(err, executor.ErrPolicyMismatch) {
		t.Fatalf("expected ErrPolicyMismatch, got %v", err)
	}
}

func TestRequiresApprovalTokenAndReleasesTheClaim(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "printf 'x'", "R2")
	_, proposalID := seed(t, h.db, c, digest)

	_, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID,
		Actor:      "worker-1",
	})
	if !errors.Is(err, executor.ErrApprovalRequired) {
		t.Fatalf("expected ErrApprovalRequired, got %v", err)
	}

	var status string
	if err := h.db.QueryRow(`SELECT status FROM proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if status != "PENDING_APPROVAL" {
		t.Fatalf("expected the proposal to be released, got %s", status)
	}
}

func TestRejectsApprovalBoundToAnotherContract(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "printf 'x'", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)

	grant, err := h.approvals.Grant(context.Background(), approval.Request{
		TaskID:         taskID,
		ProposalID:     proposalID,
		ContractDigest: strings.Repeat("f", 64),
		Approver:       "alice",
		Decision:       approval.Approved,
	})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	_, err = h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: grant.Token,
		Actor:         "worker-1",
	})
	if !errors.Is(err, approval.ErrContractMismatch) {
		t.Fatalf("expected ErrContractMismatch, got %v", err)
	}
}

func TestCannotExecuteTwice(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "printf 'x'", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	request := executor.Request{ProposalID: proposalID, ApprovalToken: token, Actor: "worker-1"}
	if _, err := h.service.Execute(context.Background(), request); err != nil {
		t.Fatalf("first execution: %v", err)
	}

	if _, err := h.service.Execute(context.Background(), request); !errors.Is(err, executor.ErrNotExecutable) {
		t.Fatalf("expected ErrNotExecutable on the second attempt, got %v", err)
	}

	var executions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records`).Scan(&executions); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if executions != 1 {
		t.Fatalf("expected exactly one execution, got %d", executions)
	}
}

func TestFailingCommandIsRecordedAsFailed(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "printf 'boom' >&2; exit 7", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusFailed {
		t.Fatalf("expected FAILED, got %s", outcome.Status)
	}

	status, stage := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusFailed) {
		t.Fatalf("expected the task to be FAILED, got %s", status)
	}
	if !stage.Valid || stage.String != "execution" {
		t.Fatalf("expected failure stage execution, got %v", stage)
	}

	var exitCode int
	if err := h.db.QueryRow(
		`SELECT exit_code FROM execution_records WHERE proposal_id = ?`, proposalID).Scan(&exitCode); err != nil {
		t.Fatalf("read execution: %v", err)
	}
	if exitCode != 7 {
		t.Fatalf("expected exit code 7, got %d", exitCode)
	}
}

func TestTimeoutIsRecordedAsUnknown(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "sleep 5", "R2")
	c.Runtime.TimeoutMS = 200
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
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusUnknown {
		t.Fatalf("a timed out action must be UNKNOWN, got %s", outcome.Status)
	}

	status, stage := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusFailed) {
		t.Fatalf("expected the task to be FAILED, got %s", status)
	}
	if !stage.Valid || stage.String != "outcome_unknown" {
		t.Fatalf("expected failure stage outcome_unknown, got %v", stage)
	}
}
func TestRunAsMismatchIsRefusedWhenEnforced(t *testing.T) {
	h := newHarnessWith(t, true)
	c, digest := buildContract(t, "printf 'restarted'", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)
	_ = h.approve(t, taskID, proposalID, digest)

	_, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID,
		Actor:      "worker-1",
	})
	if !errors.Is(err, executor.ErrRunAsUnsupported) {
		t.Fatalf("expected ErrRunAsUnsupported, got %v", err)
	}

	// Nothing ran and nothing was closed: the operator fixes the contract or
	// the runtime, they are not told the action completed.
	status, _ := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusAwaitingApproval) {
		t.Fatalf("expected the task to stay at AWAITING_APPROVAL, got %s", status)
	}
	var proposalStatus string
	if err := h.db.QueryRow(`SELECT status FROM proposals WHERE id = ?`, proposalID).Scan(&proposalStatus); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if proposalStatus != "PENDING_APPROVAL" {
		t.Fatalf("expected the proposal to stay pending, got %s", proposalStatus)
	}

	var events int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM audit_events WHERE event_type = 'execution.rejected'`,
	).Scan(&events); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if events != 1 {
		t.Fatalf("expected the refusal to be audited once, got %d", events)
	}
}

func TestExecuteUsesConfiguredIdentity(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve the current user: %v", err)
	}

	h := newHarnessRuntime(t, false, current.Username)
	c, digest := buildContract(t, "printf 'identity'", "R2")
	c.RunAs = current.Username
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	if digest, err = c.Digest(); err != nil {
		t.Fatalf("digest: %v", err)
	}

	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	outcome, err := h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (%s)", outcome.Status, outcome.Stderr)
	}

	var recorded string
	if err := h.db.QueryRow(`SELECT run_as FROM execution_records WHERE proposal_id = ?`, proposalID).Scan(&recorded); err != nil {
		t.Fatalf("read execution identity: %v", err)
	}
	if recorded != current.Username {
		t.Fatalf("recorded run_as %q, want %q", recorded, current.Username)
	}
}

func TestConfiguredIdentityMustMatchTheContract(t *testing.T) {
	current, err := user.Current()
	if err != nil {
		t.Skipf("cannot resolve the current user: %v", err)
	}
	if current.Username == "gradops-agent" {
		t.Skip("the fixture identity happens to be the current user")
	}

	h := newHarnessRuntime(t, false, current.Username)
	c, digest := buildContract(t, "printf 'x'", "R2")
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	_, err = h.service.Execute(context.Background(), executor.Request{
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if !errors.Is(err, executor.ErrRunAsUnsupported) {
		t.Fatalf("expected ErrRunAsUnsupported, got %v", err)
	}

	var executions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records`).Scan(&executions); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if executions != 0 {
		t.Fatal("an identity mismatch must not produce an execution record")
	}
	if status, _ := taskStatus(t, h.db, taskID); status != string(domain.StatusAwaitingApproval) {
		t.Fatalf("the task should be untouched, got %s", status)
	}
}

func TestRejectsObserveContractWithWrite(t *testing.T) {
	h := newHarness(t)
	c, _ := buildContract(t, "touch /var/log/app.log", "R0")
	c.Kind = string(policy.KindObserve)
	c.Paths = nil
	c.Reversible = false
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	newDigest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	_, proposalID := seed(t, h.db, c, newDigest)

	_, err = h.service.Execute(context.Background(), executor.Request{
		ProposalID: proposalID,
		Actor:      "worker-1",
	})
	if !errors.Is(err, executor.ErrUnsafeScript) {
		t.Fatalf("expected ErrUnsafeScript, got %v", err)
	}

	var executions int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records`).Scan(&executions); err != nil {
		t.Fatalf("count executions: %v", err)
	}
	if executions != 0 {
		t.Fatal("an observe contract with a write must not produce an execution record")
	}
}

func TestExecutionCeilingIsRecordedAsUnknown(t *testing.T) {
	h := newHarnessConfig(t, false, "", 100*time.Millisecond)
	c, digest := buildContract(t, "sleep 5", "R2")
	c.Runtime.TimeoutMS = 5000
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
		ProposalID:    proposalID,
		ApprovalToken: token,
		Actor:         "worker-1",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if outcome.Status != executor.StatusUnknown {
		t.Fatalf("a timed out execution must be UNKNOWN, got %s", outcome.Status)
	}

	var records int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records WHERE proposal_id = ?`, proposalID).Scan(&records); err != nil {
		t.Fatalf("count execution records: %v", err)
	}
	if records != 1 {
		t.Fatalf("expected one execution record, got %d", records)
	}
	status, stage := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusFailed) || !stage.Valid || stage.String != "outcome_unknown" {
		t.Fatalf("task status = %s stage = %v, want FAILED/outcome_unknown", status, stage)
	}
}

func TestCancelledRequestStillRecordsTheUnknownOutcome(t *testing.T) {
	h := newHarness(t)
	c, digest := buildContract(t, "sleep 5", "R2")
	c.Runtime.TimeoutMS = 5000
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	taskID, proposalID := seed(t, h.db, c, digest)
	token := h.approve(t, taskID, proposalID, digest)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		outcome executor.Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		outcome, err := h.service.Execute(ctx, executor.Request{
			ProposalID:    proposalID,
			ApprovalToken: token,
			Actor:         "worker-1",
		})
		done <- result{outcome: outcome, err: err}
	}()

	// Wait until the approval has been consumed, so cancellation happens while
	// the shell is running rather than during the claim.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var consumed int
		if err := h.db.QueryRow(
			`SELECT COUNT(*) FROM approvals WHERE proposal_id = ? AND consumed_at IS NOT NULL`,
			proposalID,
		).Scan(&consumed); err != nil {
			t.Fatalf("read approval: %v", err)
		}
		if consumed > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("approval was not consumed in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("execute: %v", got.err)
		}
		if got.outcome.Status != executor.StatusUnknown {
			t.Fatalf("a cancelled execution must be UNKNOWN, got %s", got.outcome.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("execution did not stop after cancellation")
	}

	var records int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records WHERE proposal_id = ?`, proposalID).Scan(&records); err != nil {
		t.Fatalf("count execution records: %v", err)
	}
	if records != 1 {
		t.Fatalf("the outcome must survive cancellation; got %d records", records)
	}
}
