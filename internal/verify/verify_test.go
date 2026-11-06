package verify_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/verify"
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

// seedExecuted creates a task in VERIFYING and an EXECUTED proposal whose
// postcondition is the given command.
func seedExecuted(t *testing.T, db *store.DB, postcondition contract.Postcondition) (taskID, proposalID int64) {
	t.Helper()

	now := time.Now().UTC()
	c := contract.Contract{
		Tool:          "bash",
		Kind:          "service_control",
		Script:        "printf 'did something'",
		WorkDir:       "/work",
		Env:           map[string]string{"PATH": os.Getenv("PATH")},
		Paths:         []string{"/var/log/app.log"},
		RunAs:         "gradops-agent",
		Reversible:    true,
		Runtime:       contract.Runtime{TimeoutMS: 5000, MaxOutputBytes: 4096, MaxCalls: 5},
		Postcondition: postcondition,
		Risk:          "R2",
		PolicyVersion: "test-policy",
		ApprovalLevel: "human",
		Nonce:         "0123456789abcdef0123456789abcdef",
		CreatedAt:     domain.FormatTime(now),
		ExpiresAt:     domain.FormatTime(now.Add(15 * time.Minute)),
	}
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("encode contract: %v", err)
	}

	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		stamp := domain.FormatTime(time.Now())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"restart the app", string(domain.StatusVerifying), stamp, stamp)
		if err != nil {
			return err
		}
		if taskID, err = result.LastInsertId(); err != nil {
			return err
		}
		result, err = tx.Exec(
			`INSERT INTO proposals
			    (task_id, tool, arguments, risk_level, policy_version, contract, contract_digest, status, created_at)
			 VALUES (?, 'bash', '{}', 'R2', 'test-policy', ?, ?, 'EXECUTED', ?)`,
			taskID, string(raw), digest, stamp)
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

func newService(t *testing.T, db *store.DB, workdir string) *verify.Service {
	t.Helper()

	service, err := verify.New(db, evidence.New(), verify.Config{
		Bash:    requireBash(t),
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		Timeout: 5 * time.Second,
	}, verify.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	return service
}

func TestVerificationPassesAndSealsTheTask(t *testing.T) {
	db := testDB(t)
	workdir := t.TempDir()
	taskID, proposalID := seedExecuted(t, db, contract.Postcondition{
		Command:        "printf 'active'",
		ExpectExitCode: 0,
		StdoutContains: "active",
	})

	result, err := newService(t, db, workdir).Verify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !result.Passed {
		t.Fatalf("expected the postcondition to hold, failures: %v", result.Failures)
	}
	if result.EvidenceID == "" {
		t.Fatal("verification must collect its own evidence")
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&status); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusSealed) {
		t.Fatalf("expected SEALED, got %s", status)
	}

	var passed int
	var evidenceID string
	if err := db.QueryRow(
		`SELECT passed, evidence_id FROM verification_records WHERE task_id = ? AND proposal_id = ?`,
		taskID, proposalID).Scan(&passed, &evidenceID); err != nil {
		t.Fatalf("read verification: %v", err)
	}
	if passed != 1 || evidenceID != result.EvidenceID {
		t.Fatalf("unexpected verification row: passed=%d evidence=%s", passed, evidenceID)
	}

	verified, err := audit.Verify(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit: %v", err)
	}
	if !verified.OK {
		t.Fatal("audit chain did not verify")
	}
}

func TestVerificationFailureFailsTheTask(t *testing.T) {
	db := testDB(t)
	workdir := t.TempDir()
	taskID, _ := seedExecuted(t, db, contract.Postcondition{
		Command:        "printf 'down'",
		ExpectExitCode: 0,
		StdoutContains: "active",
	})

	result, err := newService(t, db, workdir).Verify(context.Background(), taskID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.Passed {
		t.Fatal("expected the postcondition to fail")
	}
	if len(result.Failures) == 0 {
		t.Fatal("a failed verification must explain itself")
	}

	var status string
	var stage sql.NullString
	if err := db.QueryRow(`SELECT status, failure_stage FROM tasks WHERE id = ?`, taskID).Scan(&status, &stage); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusFailed) {
		t.Fatalf("expected FAILED, got %s", status)
	}
	if !stage.Valid || stage.String != "verification" {
		t.Fatalf("expected failure stage verification, got %v", stage)
	}
}

func TestVerificationRequiresAnExecutedProposal(t *testing.T) {
	db := testDB(t)
	workdir := t.TempDir()

	var taskID int64
	err := db.Tx(context.Background(), func(tx *sql.Tx) error {
		stamp := domain.FormatTime(time.Now())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"nothing ran", string(domain.StatusVerifying), stamp, stamp)
		if err != nil {
			return err
		}
		taskID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}

	if _, err := newService(t, db, workdir).Verify(context.Background(), taskID); !errors.Is(err, verify.ErrNoExecutedProposal) {
		t.Fatalf("expected ErrNoExecutedProposal, got %v", err)
	}
}

func TestVerificationRequiresTheRightTaskState(t *testing.T) {
	db := testDB(t)
	workdir := t.TempDir()
	taskID, _ := seedExecuted(t, db, contract.Postcondition{Command: "true"})

	if _, err := db.Exec(`UPDATE tasks SET status = ? WHERE id = ?`, string(domain.StatusSealed), taskID); err != nil {
		t.Fatalf("seal task: %v", err)
	}

	if _, err := newService(t, db, workdir).Verify(context.Background(), taskID); !errors.Is(err, verify.ErrNotVerifiable) {
		t.Fatalf("expected ErrNotVerifiable, got %v", err)
	}
}
