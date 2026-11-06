package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/verify"
)

// proposalDTO is the operator's view of a frozen action.
type proposalDTO struct {
	ID             int64           `json:"id"`
	TaskID         int64           `json:"task_id"`
	Tool           string          `json:"tool"`
	RiskLevel      string          `json:"risk_level"`
	PolicyVersion  string          `json:"policy_version"`
	Contract       json.RawMessage `json:"contract"`
	ContractDigest string          `json:"contract_digest"`
	Status         string          `json:"status"`
	Rationale      string          `json:"rationale"`
	CreatedAt      string          `json:"created_at"`
}

type decideRequest struct {
	Approver string `json:"approver"`
	Comment  string `json:"comment"`
	Decision string `json:"decision"`
}

type decideResponse struct {
	ApprovalID int64  `json:"approval_id"`
	Decision   string `json:"decision"`
	ExpiresAt  string `json:"expires_at"`
	// Token is returned exactly once, to the approver. It is not stored in
	// clear text and cannot be retrieved again.
	Token string `json:"token,omitempty"`
}

type executeRequest struct {
	Actor string `json:"actor"`
	Token string `json:"token"`
}

type executeResponse struct {
	ExecutionID int64  `json:"execution_id"`
	ProposalID  int64  `json:"proposal_id"`
	TaskID      int64  `json:"task_id"`
	Status      string `json:"status"`
	ExitCode    int    `json:"exit_code"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	TimedOut    bool   `json:"timed_out"`
	Truncated   bool   `json:"truncated"`
	DurationMS  int64  `json:"duration_ms"`
}

type verificationDTO struct {
	TaskID     int64    `json:"task_id"`
	ProposalID int64    `json:"proposal_id"`
	Passed     bool     `json:"passed"`
	EvidenceID string   `json:"evidence_id"`
	ExitCode   int      `json:"exit_code"`
	Stdout     string   `json:"stdout"`
	Failures   []string `json:"failures"`
	CheckedAt  string   `json:"checked_at"`
}

type evidenceDTO struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Command     string `json:"command"`
	ObservedAt  string `json:"observed_at"`
	Content     string `json:"content"`
	ContentHash string `json:"content_hash"`
	Truncated   bool   `json:"truncated"`
}

func (h *handlers) getProposal(w http.ResponseWriter, r *http.Request) {
	if h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "proposals are not configured")
		return
	}

	id, err := parsePathID(r, "proposal id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	var (
		proposal proposalDTO
		contract string
	)
	err = h.deps.DB.QueryRowContext(r.Context(),
		`SELECT id, task_id, tool, risk_level, policy_version, contract, contract_digest, status,
		        COALESCE(rationale, ''), created_at
		   FROM proposals WHERE id = ?`, id,
	).Scan(&proposal.ID, &proposal.TaskID, &proposal.Tool, &proposal.RiskLevel, &proposal.PolicyVersion,
		&contract, &proposal.ContractDigest, &proposal.Status, &proposal.Rationale, &proposal.CreatedAt)
	if err != nil {
		h.writeActionError(w, fmt.Errorf("load proposal: %w", err))
		return
	}
	if !json.Valid([]byte(contract)) {
		writeError(w, http.StatusInternalServerError, "internal_error", "stored contract is not valid JSON")
		return
	}
	proposal.Contract = json.RawMessage(contract)

	writeJSON(w, http.StatusOK, proposal)
}

// approveProposal records a human decision and returns the one-time token.
func (h *handlers) approveProposal(w http.ResponseWriter, r *http.Request) {
	if h.deps.Approvals == nil || h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "approvals are not configured")
		return
	}

	id, err := parsePathID(r, "proposal id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	var req decideRequest
	if err := h.decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	decision := approval.Approved
	switch strings.ToUpper(strings.TrimSpace(req.Decision)) {
	case "", string(approval.Approved):
	case string(approval.Rejected):
		decision = approval.Rejected
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "decision must be APPROVED or REJECTED")
		return
	}

	var (
		taskID int64
		digest string
		status string
	)
	err = h.deps.DB.QueryRowContext(r.Context(),
		`SELECT task_id, contract_digest, status FROM proposals WHERE id = ?`, id,
	).Scan(&taskID, &digest, &status)
	if err != nil {
		h.writeActionError(w, fmt.Errorf("load proposal: %w", err))
		return
	}
	if status != "PENDING_APPROVAL" {
		writeError(w, http.StatusConflict, "not_approvable",
			fmt.Sprintf("proposal is %s, not awaiting approval", status))
		return
	}

	// A proposal may only hold one live approval. An expired one does not block
	// a fresh decision, because the operator may simply have been too slow.
	var live int
	if err := h.deps.DB.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM approvals
		  WHERE proposal_id = ? AND decision = ? AND consumed_at IS NULL AND expires_at > ?`,
		id, string(approval.Approved), domain.FormatTime(time.Now().UTC()),
	).Scan(&live); err != nil {
		h.writeActionError(w, fmt.Errorf("check existing approvals: %w", err))
		return
	}
	if live > 0 {
		writeError(w, http.StatusConflict, "already_approved", "proposal already has a live approval")
		return
	}

	grant, err := h.deps.Approvals.Grant(r.Context(), approval.Request{
		TaskID:         taskID,
		ProposalID:     id,
		ContractDigest: digest,
		Approver:       req.Approver,
		Comment:        req.Comment,
		Decision:       decision,
	})
	if err != nil {
		h.writeActionError(w, err)
		return
	}

	// A rejection has to move the task, not just record an opinion: otherwise
	// the task waits forever for a decision that has already been made.
	if decision == approval.Rejected {
		if err := h.rejectTask(r.Context(), id, taskID, req.Approver); err != nil {
			h.writeActionError(w, err)
			return
		}
	}

	writeJSON(w, http.StatusCreated, decideResponse{
		ApprovalID: grant.ApprovalID,
		Decision:   string(grant.Decision),
		ExpiresAt:  domain.FormatTime(grant.ExpiresAt),
		Token:      grant.Token,
	})
}

// executeProposal runs an approved action. The approval token is the authority:
// an executor without a valid token refuses to run a privileged contract.
func (h *handlers) executeProposal(w http.ResponseWriter, r *http.Request) {
	if h.deps.Executor == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "execution is not configured")
		return
	}

	id, err := parsePathID(r, "proposal id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	var req executeRequest
	if err := h.decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	outcome, err := h.deps.Executor.Execute(r.Context(), executor.Request{
		ProposalID:    id,
		ApprovalToken: req.Token,
		Actor:         req.Actor,
	})
	if err != nil {
		h.writeActionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, executeResponse{
		ExecutionID: outcome.ExecutionID,
		ProposalID:  outcome.ProposalID,
		TaskID:      outcome.TaskID,
		Status:      outcome.Status,
		ExitCode:    outcome.ExitCode,
		Stdout:      outcome.Stdout,
		Stderr:      outcome.Stderr,
		TimedOut:    outcome.TimedOut,
		Truncated:   outcome.Truncated,
		DurationMS:  outcome.Duration.Milliseconds(),
	})
}

// verifyTask runs the contract's postcondition after execution.
func (h *handlers) verifyTask(w http.ResponseWriter, r *http.Request) {
	if h.deps.Verifier == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "verification is not configured")
		return
	}

	id, err := parsePathID(r, "task id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	result, err := h.deps.Verifier.Verify(r.Context(), id)
	if err != nil {
		h.writeActionError(w, err)
		return
	}

	failures := result.Failures
	if failures == nil {
		failures = []string{}
	}

	writeJSON(w, http.StatusOK, verificationDTO{
		TaskID:     result.TaskID,
		ProposalID: result.ProposalID,
		Passed:     result.Passed,
		EvidenceID: result.EvidenceID,
		ExitCode:   result.ExitCode,
		Stdout:     result.Stdout,
		Failures:   failures,
		CheckedAt:  domain.FormatTime(result.VerifiedAt),
	})
}

// recoverTask applies the contract's compensating action after a failed
// verification.
func (h *handlers) recoverTask(w http.ResponseWriter, r *http.Request) {
	if h.deps.Executor == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "recovery is not configured")
		return
	}

	id, err := parsePathID(r, "task id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	outcome, err := h.deps.Executor.Recover(r.Context(), id)
	if err != nil {
		h.writeActionError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, executeResponse{
		ExecutionID: outcome.ExecutionID,
		ProposalID:  outcome.ProposalID,
		TaskID:      outcome.TaskID,
		Status:      outcome.Status,
		ExitCode:    outcome.ExitCode,
		Stdout:      outcome.Stdout,
		Stderr:      outcome.Stderr,
		TimedOut:    outcome.TimedOut,
		Truncated:   outcome.Truncated,
		DurationMS:  outcome.Duration.Milliseconds(),
	})
}

func (h *handlers) listEvidence(w http.ResponseWriter, r *http.Request) {
	if h.deps.Evidence == nil || h.deps.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "evidence is not configured")
		return
	}

	id, err := parsePathID(r, "task id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	records, err := h.deps.Evidence.ListByTask(r.Context(), h.deps.DB, id)
	if err != nil {
		h.writeActionError(w, err)
		return
	}

	out := make([]evidenceDTO, 0, len(records))
	for _, record := range records {
		out = append(out, evidenceDTO{
			ID:          record.ID,
			Tool:        record.Tool,
			Command:     record.Command,
			ObservedAt:  domain.FormatTime(record.ObservedAt),
			Content:     record.Content,
			ContentHash: record.ContentHash,
			Truncated:   record.Truncated,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// writeActionError maps control-loop failures onto HTTP without leaking
// internal detail. Anything unrecognised stays a 500 and is logged.
func (h *handlers) writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, executor.ErrProposalNotFound), errors.Is(err, verify.ErrNoExecutedProposal),
		errors.Is(err, evidence.ErrNotFound), errors.Is(err, domain.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", err.Error())

	case errors.Is(err, executor.ErrApprovalRequired):
		writeError(w, http.StatusForbidden, "approval_required", err.Error())

	case errors.Is(err, approval.ErrExpired), errors.Is(err, approval.ErrAlreadyConsumed),
		errors.Is(err, approval.ErrRejected), errors.Is(err, approval.ErrContractMismatch),
		errors.Is(err, executor.ErrContractTampered), errors.Is(err, executor.ErrPolicyMismatch),
		errors.Is(err, executor.ErrNotExecutable), errors.Is(err, executor.ErrExpired),
		errors.Is(err, executor.ErrNoCompensation), errors.Is(err, executor.ErrNotRecoverable),
		errors.Is(err, verify.ErrNotVerifiable):
		writeError(w, http.StatusConflict, "conflict", err.Error())

	case errors.Is(err, approval.ErrInvalidRequest), errors.Is(err, executor.ErrInvalidRequest),
		errors.Is(err, verify.ErrInvalidRequest), errors.Is(err, domain.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())

	default:
		h.deps.Logger.Error("action failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
	}
}

// rejectTask moves the task and its proposal out of the approval state. The
// approval row already records who decided what; this is the state change that
// makes the decision take effect.
func (h *handlers) rejectTask(ctx context.Context, proposalID, taskID int64, approver string) error {
	now := time.Now().UTC()

	return h.deps.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE proposals SET status = 'REJECTED' WHERE id = ? AND status = 'PENDING_APPROVAL'`,
			proposalID); err != nil {
			return fmt.Errorf("reject proposal: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, failure_stage = 'approval', updated_at = ?
			  WHERE id = ? AND status = ?`,
			string(domain.StatusRejected), domain.FormatTime(now),
			taskID, string(domain.StatusAwaitingApproval)); err != nil {
			return fmt.Errorf("reject task: %w", err)
		}

		payload, err := json.Marshal(map[string]any{"proposal_id": proposalID, "approver": approver})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     approver,
			Type:      "task.rejected",
			Message:   fmt.Sprintf("proposal %d was rejected", proposalID),
			Payload:   payload,
			CreatedAt: now,
		})
	})
}
