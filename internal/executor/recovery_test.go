package executor_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/store"
)

// seedRecoverable stores a task that already failed verification and a proposal
// that already ran, then moves both into the states a rollback starts from.
func seedRecoverable(t *testing.T, db *store.DB, compensation *contract.Compensation) (int64, int64) {
	t.Helper()

	c, _ := buildContract(t, "printf 'restarted'", "R2")
	c.Compensation = compensation
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

	var taskID, proposalID int64
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		stamp := domain.FormatTime(time.Now().UTC())
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"restart the app", string(domain.StatusAwaitingApproval), stamp, stamp)
		if err != nil {
			return err
		}
		if taskID, err = result.LastInsertId(); err != nil {
			return err
		}

		result, err = tx.Exec(
			`INSERT INTO proposals
			    (task_id, tool, arguments, risk_level, policy_version, contract, contract_digest, status, created_at)
			 VALUES (?, 'bash', '{}', ?, ?, ?, ?, 'PENDING_APPROVAL', ?)`,
			taskID, c.Risk, c.PolicyVersion, string(raw), digest, stamp)
		if err != nil {
			return err
		}
		if proposalID, err = result.LastInsertId(); err != nil {
			return err
		}

		if _, err := tx.Exec(`UPDATE proposals SET status = 'EXECUTED' WHERE id = ?`, proposalID); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE tasks SET status = ? WHERE id = ?`,
			string(domain.StatusRollingBack), taskID)
		return err
	})
	if err != nil {
		t.Fatalf("seed recoverable: %v", err)
	}
	return taskID, proposalID
}

func TestRecoveryAppliesTheCompensation(t *testing.T) {
	h := newHarness(t)
	taskID, proposalID := seedRecoverable(t, h.db, &contract.Compensation{
		Command:        "printf 'undone'",
		StdoutContains: "undone",
	})

	outcome, err := h.service.Recover(context.Background(), taskID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if outcome.Status != executor.StatusRecovered {
		t.Fatalf("expected RECOVERED, got %s (%s)", outcome.Status, outcome.Stderr)
	}

	status, stage := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusRolledBack) {
		t.Fatalf("expected ROLLED_BACK, got %s", status)
	}
	if stage.Valid {
		t.Fatalf("expected no failure stage after a successful rollback, got %s", stage.String)
	}

	var recordStatus string
	if err := h.db.QueryRow(`SELECT outcome FROM execution_records WHERE proposal_id = ?`, proposalID).Scan(&recordStatus); err != nil {
		t.Fatalf("read execution record: %v", err)
	}
	if recordStatus != executor.StatusRecovered {
		t.Fatalf("expected the execution record to say RECOVERED, got %s", recordStatus)
	}

	var events int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM audit_events WHERE task_id = ? AND event_type = 'recovery.completed'`, taskID,
	).Scan(&events); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if events != 1 {
		t.Fatalf("expected one recovery.completed event, got %d", events)
	}

	var evidenceRows int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM evidence WHERE task_id = ? AND tool = 'recovery'`, taskID,
	).Scan(&evidenceRows); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evidenceRows != 1 {
		t.Fatalf("expected one recovery evidence row, got %d", evidenceRows)
	}
}

func TestRecoveryFailureIsRecordedAsSuch(t *testing.T) {
	h := newHarness(t)
	taskID, _ := seedRecoverable(t, h.db, &contract.Compensation{
		Command:        "printf 'not what we wanted'",
		StdoutContains: "undone",
	})

	outcome, err := h.service.Recover(context.Background(), taskID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if outcome.Status != executor.StatusRecoveryFailed {
		t.Fatalf("expected RECOVERY_FAILED, got %s", outcome.Status)
	}

	status, stage := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusRecoveryFailed) {
		t.Fatalf("expected RECOVERY_FAILED task, got %s", status)
	}
	if !stage.Valid || stage.String != "recovery" {
		t.Fatalf("expected failure_stage=recovery, got %v", stage)
	}
}

func TestRecoveryRequiresACompensation(t *testing.T) {
	h := newHarness(t)
	taskID, _ := seedRecoverable(t, h.db, nil)

	if _, err := h.service.Recover(context.Background(), taskID); !errors.Is(err, executor.ErrNoCompensation) {
		t.Fatalf("expected ErrNoCompensation, got %v", err)
	}
}

func TestRecoveryRefusesATaskThatIsNotRollingBack(t *testing.T) {
	h := newHarness(t)
	taskID, _ := seedRecoverable(t, h.db, &contract.Compensation{Command: "true"})

	if _, err := h.db.Exec(`UPDATE tasks SET status = ? WHERE id = ?`,
		string(domain.StatusSealed), taskID); err != nil {
		t.Fatalf("update task: %v", err)
	}

	if _, err := h.service.Recover(context.Background(), taskID); !errors.Is(err, executor.ErrNotRecoverable) {
		t.Fatalf("expected ErrNotRecoverable, got %v", err)
	}
}

func TestRecoveryCannotRunTwice(t *testing.T) {
	h := newHarness(t)
	taskID, _ := seedRecoverable(t, h.db, &contract.Compensation{
		Command:        "printf 'undone'",
		StdoutContains: "undone",
	})

	if _, err := h.service.Recover(context.Background(), taskID); err != nil {
		t.Fatalf("first recovery: %v", err)
	}
	if _, err := h.service.Recover(context.Background(), taskID); !errors.Is(err, executor.ErrNotRecoverable) {
		t.Fatalf("expected the second rollback to be refused, got %v", err)
	}
}

func TestRecoveryCeilingIsRecordedAsFailure(t *testing.T) {
	h := newHarnessConfig(t, false, "", 100*time.Millisecond)
	taskID, proposalID := seedRecoverable(t, h.db, &contract.Compensation{
		Command: "sleep 5",
	})

	outcome, err := h.service.Recover(context.Background(), taskID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if outcome.Status != executor.StatusRecoveryFailed {
		t.Fatalf("expected RECOVERY_FAILED, got %s", outcome.Status)
	}

	var records int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM execution_records WHERE proposal_id = ?`, proposalID).Scan(&records); err != nil {
		t.Fatalf("count records: %v", err)
	}
	if records != 1 {
		t.Fatalf("expected the rollback outcome to be recorded, got %d records", records)
	}
	status, _ := taskStatus(t, h.db, taskID)
	if status != string(domain.StatusRecoveryFailed) {
		t.Fatalf("task status = %s, want RECOVERY_FAILED", status)
	}
}
