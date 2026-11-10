package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
)

var (
	// ErrNoCompensation means the contract does not define an undo.
	ErrNoCompensation = errors.New("contract has no compensating action")
	// ErrNotRecoverable means the task is not waiting for a rollback.
	ErrNotRecoverable = errors.New("task is not awaiting recovery")
)

// Recovery outcomes.
const (
	StatusRecovered      = "RECOVERED"
	StatusRecoveryFailed = "RECOVERY_FAILED"
)

// Recover runs the compensating action of a task whose verification failed.
//
// The compensation was part of the approved contract, so it runs without a
// second token: the approver approved the undo together with the action. It is
// still a side effect, so it goes through the same session bounds, writes the
// same kind of records, and can only be started once.
func (s *Service) Recover(ctx context.Context, taskID int64) (Outcome, error) {
	if taskID <= 0 {
		return Outcome{}, fmt.Errorf("%w: task is required", ErrInvalidRequest)
	}

	var taskStatus string
	if err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&taskStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Outcome{}, fmt.Errorf("task %d: %w", taskID, domain.ErrNotFound)
		}
		return Outcome{}, fmt.Errorf("load task: %w", err)
	}
	if domain.Status(taskStatus) != domain.StatusRollingBack {
		return Outcome{}, fmt.Errorf("%w: task is %s", ErrNotRecoverable, taskStatus)
	}

	proposalID, digest, c, err := s.loadExecutedProposal(ctx, taskID)
	if err != nil {
		return Outcome{}, err
	}
	if err := contract.Verify(c, digest); err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrContractTampered, err)
	}
	if c.Compensation == nil {
		return Outcome{}, ErrNoCompensation
	}

	// Claim the rollback. The proposal can leave EXECUTED exactly once.
	claimed, err := s.claimRollback(ctx, proposalID)
	if err != nil {
		return Outcome{}, err
	}
	if !claimed {
		return Outcome{}, fmt.Errorf("%w: rollback already started", ErrNotRecoverable)
	}

	session, err := s.session(c)
	if err != nil {
		return Outcome{}, fmt.Errorf("prepare recovery shell: %w", err)
	}

	runCtx, cancelRun := context.WithTimeout(ctx, s.executionTimeout)
	defer cancelRun()

	started := s.now().UTC()
	compensation := c.Compensation
	result, runErr := session.Run(runCtx, compensation.Command)
	finished := s.now().UTC()

	outcome := Outcome{
		ProposalID: proposalID,
		TaskID:     taskID,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		ExitCode:   result.ExitCode,
		TimedOut:   result.TimedOut,
		Truncated:  result.Truncated(),
		Duration:   finished.Sub(started),
	}

	passed := runErr == nil && !result.TimedOut &&
		result.ExitCode == compensation.ExpectExitCode &&
		(compensation.StdoutContains == "" || strings.Contains(result.Stdout, compensation.StdoutContains))

	switch {
	case passed:
		outcome.Status = StatusRecovered
	case ctx.Err() != nil || runCtx.Err() != nil:
		outcome.Status = StatusRecoveryFailed
		if runCtx.Err() != nil {
			outcome.Stderr = runCtx.Err().Error()
		} else {
			outcome.Stderr = ctx.Err().Error()
		}
	default:
		outcome.Status = StatusRecoveryFailed
	}

	// A rollback is a side effect too. Persist its result even if the caller
	// went away while the compensation was running.
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelRecord()
	if err := s.recordRecovery(recordCtx, proposalID, taskID, c, outcome, started, finished); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func (s *Service) loadExecutedProposal(ctx context.Context, taskID int64) (int64, string, contract.Contract, error) {
	var (
		id   int64
		raw  string
		hash string
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, contract, contract_digest FROM proposals
		  WHERE task_id = ? AND status = 'EXECUTED'
		  ORDER BY id DESC LIMIT 1`, taskID,
	).Scan(&id, &raw, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", contract.Contract{}, fmt.Errorf("task %d: %w", taskID, ErrProposalNotFound)
	}
	if err != nil {
		return 0, "", contract.Contract{}, fmt.Errorf("load proposal: %w", err)
	}

	var c contract.Contract
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return 0, "", contract.Contract{}, fmt.Errorf("decode contract: %w", err)
	}
	return id, hash, c, nil
}

func (s *Service) claimRollback(ctx context.Context, proposalID int64) (bool, error) {
	var claimed bool
	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE proposals SET status = 'ROLLING_BACK' WHERE id = ? AND status = 'EXECUTED'`, proposalID)
		if err != nil {
			return fmt.Errorf("claim rollback: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("claim rollback: %w", err)
		}
		claimed = affected == 1
		return nil
	})
	return claimed, err
}

func (s *Service) recordRecovery(ctx context.Context, proposalID, taskID int64, c contract.Contract, outcome Outcome, started, finished time.Time) error {
	taskStatus := domain.StatusRolledBack
	failureStage := ""
	if outcome.Status == StatusRecoveryFailed {
		taskStatus = domain.StatusRecoveryFailed
		failureStage = "recovery"
	}

	runtimeJSON, err := json.Marshal(c.Runtime)
	if err != nil {
		return fmt.Errorf("encode runtime: %w", err)
	}

	truncated := 0
	if outcome.Truncated {
		truncated = 1
	}

	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`INSERT INTO execution_records
			    (proposal_id, outcome, exit_code, stdout_digest, stderr_digest,
			     output_truncated, run_as, runtime, started_at, finished_at, duration_ms)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			proposalID, outcome.Status, outcome.ExitCode,
			evidence.Hash(outcome.Stdout), evidence.Hash(outcome.Stderr),
			truncated, c.RunAs, string(runtimeJSON),
			domain.FormatTime(started), domain.FormatTime(finished),
			outcome.Duration.Milliseconds())
		if err != nil {
			return fmt.Errorf("insert recovery record: %w", err)
		}
		if outcome.ExecutionID, err = result.LastInsertId(); err != nil {
			return fmt.Errorf("recovery id: %w", err)
		}

		record := evidence.Record{
			TaskID:     taskID,
			Tool:       "recovery",
			Command:    c.Compensation.Command,
			ObservedAt: finished,
			Content:    renderOutput(outcome),
			Truncated:  outcome.Truncated,
		}
		if err := s.evidence.Save(ctx, tx, &record); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, failure_stage = ?, updated_at = ?
			  WHERE id = ? AND status = ?`,
			string(taskStatus), nullable(failureStage), domain.FormatTime(finished),
			taskID, string(domain.StatusRollingBack)); err != nil {
			return fmt.Errorf("update task: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"proposal_id":  proposalID,
			"execution_id": outcome.ExecutionID,
			"outcome":      outcome.Status,
			"exit_code":    outcome.ExitCode,
			"evidence_id":  record.ID,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}

		eventType := "recovery.completed"
		message := fmt.Sprintf("compensation of proposal %d applied", proposalID)
		if outcome.Status == StatusRecoveryFailed {
			eventType = "recovery.failed"
			message = fmt.Sprintf("compensation of proposal %d did not hold", proposalID)
		}

		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "recovery",
			Type:      eventType,
			Message:   message,
			Payload:   payload,
			CreatedAt: finished,
		})
	})
}
