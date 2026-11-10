// Package executor runs an approved action, and is the only place in the
// control plane that changes the host.
//
// Everything before it is advisory: an investigation can propose, policy can
// classify, a human can approve. The executor re-derives all of it from the
// stored contract immediately before running, so an action that was approved
// cannot become a different action by editing a row in between.
package executor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/user"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/command"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/shell"
	"github.com/gdemonc/grad-ops/internal/store"
)

var (
	// ErrProposalNotFound means no proposal has that id.
	ErrProposalNotFound = errors.New("proposal not found")
	// ErrContractTampered means the stored contract no longer matches its digest.
	ErrContractTampered = errors.New("contract does not match its digest")
	// ErrPolicyMismatch means the frozen risk no longer matches the policy.
	ErrPolicyMismatch = errors.New("contract risk disagrees with current policy")
	// ErrNotExecutable means the proposal or its task is not in a state that
	// may start executing.
	ErrNotExecutable = errors.New("proposal is not executable in its current state")
	// ErrApprovalRequired means a token is needed and none was supplied.
	ErrApprovalRequired = errors.New("approval token required")
	// ErrExpired means the contract's validity window has passed.
	ErrExpired = errors.New("contract expired")
	// ErrInvalidRequest means the caller supplied an unusable request.
	ErrInvalidRequest = errors.New("invalid execution request")
	// ErrRunAsUnsupported means the contract asks for an identity this process
	// cannot assume, and enforcement is on.
	ErrRunAsUnsupported = errors.New("the contract requires an identity this process cannot assume")
	// ErrUnsafeScript means the script's recognized effects contradict its
	// declared kind. The contract is refused before anything runs.
	ErrUnsafeScript = errors.New("script has effects its declared kind does not allow")
)

// Outcome statuses.
const (
	StatusSucceeded = "SUCCEEDED"
	StatusFailed    = "FAILED"
	StatusUnknown   = "UNKNOWN"
)

// Request asks to execute one proposal.
type Request struct {
	ProposalID    int64
	ApprovalToken string
	Actor         string
}

// Outcome describes what happened.
type Outcome struct {
	ExecutionID int64
	ProposalID  int64
	TaskID      int64
	Status      string
	ExitCode    int
	Stdout      string
	Stderr      string
	TimedOut    bool
	Truncated   bool
	Duration    time.Duration
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces the clock.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithWorkDirMapper translates a contract path to a host path.
//
// On Linux this is the identity. A development host can map the contract's
// POSIX paths onto a scratch directory so the executor can be exercised there;
// it does not make the product portable.
func WithWorkDirMapper(mapper func(string) string) Option {
	return func(s *Service) { s.mapWorkDir = mapper }
}

// Service executes approved proposals.
type Service struct {
	db         *store.DB
	policy     *policy.Engine
	approvals  *approval.Service
	evidence   *evidence.Store
	bash       string
	baseEnv    map[string]string
	now        func() time.Time
	mapWorkDir func(string) string
	// requireRunAsMatch refuses contracts whose run_as this process cannot
	// become. It is the fail-closed alternative to silently running an action
	// as the service account while its contract says otherwise.
	requireRunAsMatch bool
	// runAs is the identity the executor was configured to assume. When set,
	// the contract must name the same identity and the shell switches to it.
	runAs            string
	limits           shell.Limits
	executionTimeout time.Duration
	cgroup           shell.CgroupConfig
	sandbox          shell.SandboxConfig
}

// Config is the executor's fixed configuration.
type Config struct {
	// Bash is the absolute path of the shell that will run action scripts.
	Bash string
	// BaseEnv is merged under the contract environment. The contract wins on
	// conflict, because it is what the approver saw.
	BaseEnv map[string]string
	// RequireRunAsMatch refuses to run a contract whose run_as differs from the
	// process identity. Without a Linux runtime the process cannot switch user,
	// so leaving this off means the contract's identity is aspirational.
	RequireRunAsMatch bool
	// RunAs is the identity the executor should assume for every action. When
	// set, the contract's run_as must match it, and the shell actually switches
	// to it (or startup fails).
	RunAs string
	// Limits bounds each action's shell process.
	Limits shell.Limits
	// ExecutionTimeout is an emergency ceiling on one execution, on top of the
	// contract's own runtime timeout. Zero uses the default.
	ExecutionTimeout time.Duration
	// Cgroup is the optional cgroup v2 boundary for the action shell.
	Cgroup shell.CgroupConfig
	// Sandbox is the optional bubblewrap namespace/filesystem boundary.
	Sandbox shell.SandboxConfig
}

// New builds a service.
func New(db *store.DB, engine *policy.Engine, approvals *approval.Service, ev *evidence.Store, cfg Config, opts ...Option) (*Service, error) {
	if db == nil || engine == nil || approvals == nil || ev == nil {
		return nil, fmt.Errorf("%w: database, policy, approvals and evidence are required", ErrInvalidRequest)
	}
	if strings.TrimSpace(cfg.Bash) == "" {
		return nil, fmt.Errorf("%w: a shell path is required", ErrInvalidRequest)
	}

	service := &Service{
		db:                db,
		policy:            engine,
		approvals:         approvals,
		evidence:          ev,
		bash:              cfg.Bash,
		baseEnv:           cfg.BaseEnv,
		now:               time.Now,
		mapWorkDir:        func(path string) string { return path },
		requireRunAsMatch: cfg.RequireRunAsMatch,
		runAs:             strings.TrimSpace(cfg.RunAs),
		limits:            cfg.Limits,
		executionTimeout:  cfg.ExecutionTimeout,
		cgroup:            cfg.Cgroup,
		sandbox:           cfg.Sandbox,
	}
	if service.executionTimeout <= 0 {
		service.executionTimeout = 2 * time.Minute
	}
	if service.executionTimeout > 24*time.Hour {
		return nil, fmt.Errorf("%w: execution timeout must not exceed 24h", ErrInvalidRequest)
	}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

type storedProposal struct {
	ID       int64
	TaskID   int64
	Contract contract.Contract
	Digest   string
	Status   string
}

// Execute runs the proposal's contract if every precondition still holds.
func (s *Service) Execute(ctx context.Context, req Request) (Outcome, error) {
	if req.ProposalID <= 0 || strings.TrimSpace(req.Actor) == "" {
		return Outcome{}, fmt.Errorf("%w: proposal and actor are required", ErrInvalidRequest)
	}

	proposal, err := s.loadProposal(ctx, req.ProposalID)
	if err != nil {
		return Outcome{}, err
	}

	// 1. The stored contract must still hash to the digest that was approved.
	if err := contract.Verify(proposal.Contract, proposal.Digest); err != nil {
		s.reject(ctx, proposal, req.Actor, "contract digest mismatch", err)
		return Outcome{}, fmt.Errorf("%w: %v", ErrContractTampered, err)
	}
	if contract.DigestScript(proposal.Contract.Script) != proposal.Contract.ScriptDigest {
		s.reject(ctx, proposal, req.Actor, "script digest mismatch", nil)
		return Outcome{}, fmt.Errorf("%w: script digest mismatch", ErrContractTampered)
	}

	// 2. A contract declared read-only must not contain a write, an indirect
	// execution path or a command name assembled at runtime. This is a second
	// opinion; policy and the runtime still bound an action that passes it.
	analysis, err := command.Analyze(proposal.Contract.Script)
	if err != nil {
		s.reject(ctx, proposal, req.Actor, "script cannot be parsed", err)
		return Outcome{}, fmt.Errorf("%w: %v", ErrUnsafeScript, err)
	}
	if proposal.Contract.Kind == string(policy.KindObserve) && !analysis.ReadOnly() {
		s.reject(ctx, proposal, req.Actor, "script effects disagree with declared kind", nil)
		return Outcome{}, fmt.Errorf("%w: %s", ErrUnsafeScript, analysis.Summary())
	}

	// 3. Policy must still reach the same risk level. A contract that claims a
	// lower risk than the current rules allow is refused, not downgraded.
	evaluation := s.policy.Evaluate(policy.Request{
		Subject:    policy.Subject{ID: req.Actor},
		Kind:       policy.Kind(proposal.Contract.Kind),
		Command:    proposal.Contract.Script,
		Paths:      proposal.Contract.Paths,
		RunAs:      proposal.Contract.RunAs,
		Reversible: proposal.Contract.Reversible,
		PolicyRef:  proposal.Contract.PolicyRef,
	})
	frozenRisk, err := domain.ParseRiskLevel(proposal.Contract.Risk)
	if err != nil {
		return Outcome{}, fmt.Errorf("%w: %v", ErrContractTampered, err)
	}
	if evaluation.Risk != frozenRisk {
		s.reject(ctx, proposal, req.Actor, "risk disagrees with policy", nil)
		return Outcome{}, fmt.Errorf("%w: contract says %s, policy says %s", ErrPolicyMismatch, frozenRisk, evaluation.Risk)
	}
	if evaluation.Decision == policy.Deny {
		s.reject(ctx, proposal, req.Actor, "policy denies the action", nil)
		return Outcome{}, fmt.Errorf("%w: %s", ErrPolicyMismatch, evaluation.Reason)
	}

	// 2.5 Without a runtime that can switch user, the requested identity cannot
	// be assumed. When enforcement is on, that is a refusal rather than an
	// action quietly run as the service account.
	if s.requireRunAsMatch && !identityMatches(proposal.Contract.RunAs) {
		s.reject(ctx, proposal, req.Actor, "run_as cannot be assumed by this process", nil)
		return Outcome{}, fmt.Errorf("%w: contract asks for %q", ErrRunAsUnsupported, proposal.Contract.RunAs)
	}

	// 2.6 A configured runtime identity is authoritative. A contract naming a
	// different user is refused rather than executed as the configured user
	// while claiming otherwise.
	if s.runAs != "" && strings.TrimSpace(proposal.Contract.RunAs) != s.runAs {
		s.reject(ctx, proposal, req.Actor, "run_as differs from the configured executor identity", nil)
		return Outcome{}, fmt.Errorf("%w: contract asks for %q, executor runs as %q",
			ErrRunAsUnsupported, proposal.Contract.RunAs, s.runAs)
	}

	// 3. The window the approver saw must still be open.
	now := s.now().UTC()
	if proposal.Contract.Expired(now) {
		return Outcome{}, ErrExpired
	}

	// 4. Claim the proposal and the task atomically.
	previous := proposal.Status
	claimed, err := s.claim(ctx, proposal, req.Actor, now)
	if err != nil {
		return Outcome{}, err
	}
	if !claimed {
		return Outcome{}, ErrNotExecutable
	}

	// 5. Spend the approval. A failure here puts the proposal back where it was
	// so the operator can retry with the right token. Nothing has run yet.
	if evaluation.RequiresApproval {
		if strings.TrimSpace(req.ApprovalToken) == "" {
			s.release(ctx, proposal.ID, proposal.TaskID, previous)
			return Outcome{}, ErrApprovalRequired
		}
		if _, err := s.approvals.Consume(ctx, req.ApprovalToken, proposal.Digest); err != nil {
			s.release(ctx, proposal.ID, proposal.TaskID, previous)
			return Outcome{}, err
		}
	}

	session, err := s.session(proposal.Contract)
	if err != nil {
		s.release(ctx, proposal.ID, proposal.TaskID, previous)
		return Outcome{}, fmt.Errorf("prepare shell: %w", err)
	}

	// The contract carries its own runtime timeout. This ceiling also covers a
	// shell that starts but does not make progress, and it is deliberately
	// separate from the request context: a client disconnect must not prevent
	// the outcome from being recorded.
	runCtx, cancelRun := context.WithTimeout(ctx, s.executionTimeout)
	defer cancelRun()

	started := s.now().UTC()
	result, runErr := session.Run(runCtx, proposal.Contract.Script)
	finished := s.now().UTC()

	outcome := Outcome{
		ProposalID: proposal.ID,
		TaskID:     proposal.TaskID,
		Status:     StatusSucceeded,
		ExitCode:   result.ExitCode,
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		TimedOut:   result.TimedOut,
		Truncated:  result.Truncated(),
		Duration:   finished.Sub(started),
	}

	switch {
	case runErr != nil && errors.Is(runErr, shell.ErrCleanupFailed):
		// The script may have completed, but its cgroup could not be killed or
		// removed. Containment is unproven, so do not report success.
		outcome.Status = StatusUnknown
		outcome.Stderr = runErr.Error()
	case runErr != nil && (ctx.Err() != nil || runCtx.Err() != nil):
		// The process was interrupted while the script may have been running.
		outcome.Status = StatusUnknown
	case runErr != nil:
		// The shell never started: nothing ran, so this is a failure, not an
		// unknown outcome.
		outcome.Status = StatusFailed
		outcome.Stderr = runErr.Error()
	case result.TimedOut:
		outcome.Status = StatusUnknown
	case result.ExitCode != 0:
		outcome.Status = StatusFailed
	}

	// The side effect may already have happened. Record it even when the
	// request context was cancelled; otherwise the database would claim that
	// nothing ran.
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelRecord()
	if err := s.record(recordCtx, proposal, outcome, started, finished, req.Actor); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func (s *Service) loadProposal(ctx context.Context, id int64) (storedProposal, error) {
	var (
		proposal storedProposal
		raw      string
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, task_id, contract, contract_digest, status FROM proposals WHERE id = ?`, id,
	).Scan(&proposal.ID, &proposal.TaskID, &raw, &proposal.Digest, &proposal.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return storedProposal{}, ErrProposalNotFound
	}
	if err != nil {
		return storedProposal{}, fmt.Errorf("load proposal: %w", err)
	}

	if err := json.Unmarshal([]byte(raw), &proposal.Contract); err != nil {
		return storedProposal{}, fmt.Errorf("decode contract: %w", err)
	}
	return proposal, nil
}

// claim moves the proposal and its task into EXECUTING, or reports that it
// could not. Both updates are conditional, so a second executor cannot start
// the same action.
func (s *Service) claim(ctx context.Context, proposal storedProposal, actor string, now time.Time) (bool, error) {
	claimed := false

	err := s.db.Tx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE proposals SET status = 'EXECUTING'
			  WHERE id = ? AND status IN ('PENDING_APPROVAL', 'APPROVED')`,
			proposal.ID)
		if err != nil {
			return fmt.Errorf("claim proposal: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("claim proposal: %w", err)
		}
		if affected != 1 {
			return nil
		}

		result, err = tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, updated_at = ?
			  WHERE id = ? AND status = ?`,
			string(domain.StatusExecuting), domain.FormatTime(now),
			proposal.TaskID, string(domain.StatusAwaitingApproval))
		if err != nil {
			return fmt.Errorf("claim task: %w", err)
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("claim task: %w", err)
		}
		if affected != 1 {
			// Put the proposal back: the task was not in the state this
			// execution required.
			_, revertErr := tx.ExecContext(ctx,
				`UPDATE proposals SET status = ? WHERE id = ? AND status = 'EXECUTING'`,
				proposal.Status, proposal.ID)
			return revertErr
		}

		payload, err := json.Marshal(map[string]any{
			"proposal_id":     proposal.ID,
			"contract_digest": proposal.Digest,
			"risk":            proposal.Contract.Risk,
			"kind":            proposal.Contract.Kind,
			"script_digest":   proposal.Contract.ScriptDigest,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}

		taskID := proposal.TaskID
		if err := audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     actor,
			Type:      "execution.started",
			Message:   fmt.Sprintf("executing proposal %d", proposal.ID),
			Payload:   payload,
			CreatedAt: now,
		}); err != nil {
			return err
		}

		claimed = true
		return nil
	})
	return claimed, err
}

// release undoes a claim on a proposal that never ran.
//
// Both rows are restored: leaving the task in EXECUTING after a claim that was
// abandoned would make every later attempt fail on a state nothing is in.
func (s *Service) release(ctx context.Context, proposalID, taskID int64, previous string) {
	_ = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE proposals SET status = ? WHERE id = ? AND status = 'EXECUTING'`,
			previous, proposalID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ? WHERE id = ? AND status = 'EXECUTING'`,
			string(domain.StatusAwaitingApproval), taskID)
		return err
	})
}

// record stores the execution, its output as evidence, the new statuses and the
// audit event, all in one transaction.
func (s *Service) record(ctx context.Context, proposal storedProposal, outcome Outcome, started, finished time.Time, actor string) error {
	proposalStatus := map[string]string{
		StatusSucceeded: "EXECUTED",
		StatusFailed:    "FAILED",
		StatusUnknown:   "UNKNOWN",
	}[outcome.Status]

	taskStatus := domain.StatusVerifying
	failureStage := ""
	switch outcome.Status {
	case StatusFailed:
		taskStatus = domain.StatusFailed
		failureStage = "execution"
	case StatusUnknown:
		taskStatus = domain.StatusFailed
		failureStage = "outcome_unknown"
	}

	runtimeJSON, err := json.Marshal(proposal.Contract.Runtime)
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
			proposal.ID, outcome.Status, outcome.ExitCode,
			evidence.Hash(outcome.Stdout), evidence.Hash(outcome.Stderr),
			truncated, proposal.Contract.RunAs, string(runtimeJSON),
			domain.FormatTime(started), domain.FormatTime(finished),
			outcome.Duration.Milliseconds())
		if err != nil {
			return fmt.Errorf("insert execution record: %w", err)
		}
		if outcome.ExecutionID, err = result.LastInsertId(); err != nil {
			return fmt.Errorf("execution id: %w", err)
		}

		record := evidence.Record{
			TaskID:     proposal.TaskID,
			Tool:       "bash",
			Command:    proposal.Contract.Script,
			ObservedAt: finished,
			Content:    renderOutput(outcome),
			Truncated:  outcome.Truncated,
		}
		if err := s.evidence.Save(ctx, tx, &record); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE proposals SET status = ? WHERE id = ? AND status = 'EXECUTING'`,
			proposalStatus, proposal.ID); err != nil {
			return fmt.Errorf("update proposal: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, failure_stage = ?, updated_at = ?
			  WHERE id = ? AND status = ?`,
			string(taskStatus), nullable(failureStage), domain.FormatTime(finished),
			proposal.TaskID, string(domain.StatusExecuting)); err != nil {
			return fmt.Errorf("update task: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"proposal_id":  proposal.ID,
			"execution_id": outcome.ExecutionID,
			"outcome":      outcome.Status,
			"exit_code":    outcome.ExitCode,
			"duration_ms":  outcome.Duration.Milliseconds(),
			"timed_out":    outcome.TimedOut,
			"truncated":    outcome.Truncated,
			"evidence_id":  record.ID,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}

		taskID := proposal.TaskID
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     actor,
			Type:      eventType(outcome.Status),
			Message:   fmt.Sprintf("proposal %d finished %s", proposal.ID, outcome.Status),
			Payload:   payload,
			CreatedAt: finished,
		})
	})
}

// reject records a refusal in the audit chain. The task is untouched: nothing
// ran, so its state is still whatever the previous step left.
func (s *Service) reject(ctx context.Context, proposal storedProposal, actor, reason string, cause error) {
	payload, _ := json.Marshal(map[string]any{
		"proposal_id":     proposal.ID,
		"contract_digest": proposal.Digest,
		"reason":          reason,
	})
	if cause != nil {
		payload, _ = json.Marshal(map[string]any{
			"proposal_id":     proposal.ID,
			"contract_digest": proposal.Digest,
			"reason":          reason,
			"error":           cause.Error(),
		})
	}

	_ = s.db.Tx(ctx, func(tx *sql.Tx) error {
		taskID := proposal.TaskID
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     actor,
			Type:      "execution.rejected",
			Message:   fmt.Sprintf("proposal %d refused: %s", proposal.ID, reason),
			Payload:   payload,
			CreatedAt: s.now().UTC(),
		})
	})
}

func (s *Service) session(c contract.Contract) (*shell.Session, error) {
	env := make(map[string]string, len(s.baseEnv)+len(c.Env))
	for key, value := range s.baseEnv {
		env[key] = value
	}
	for key, value := range c.Env {
		env[key] = value
	}

	return shell.NewSession(shell.Config{
		Executable:     s.bash,
		WorkDir:        s.mapWorkDir(c.WorkDir),
		Env:            env,
		Timeout:        time.Duration(c.Runtime.TimeoutMS) * time.Millisecond,
		MaxOutputBytes: int(c.Runtime.MaxOutputBytes),
		MaxCalls:       int(c.Runtime.MaxCalls),
		RunAs:          s.runAs,
		Limits:         s.limits,
		Cgroup:         s.cgroup,
		Sandbox:        s.sandbox,
	})
}

func eventType(status string) string {
	switch status {
	case StatusSucceeded:
		return "execution.completed"
	case StatusUnknown:
		return "execution.unknown"
	default:
		return "execution.failed"
	}
}

func renderOutput(outcome Outcome) string {
	var builder strings.Builder
	builder.WriteString("stdout:\n")
	builder.WriteString(outcome.Stdout)
	builder.WriteString("\nstderr:\n")
	builder.WriteString(outcome.Stderr)
	if outcome.TimedOut {
		builder.WriteString("\n[timed out]")
	}
	if outcome.Truncated {
		builder.WriteString("\n[output truncated]")
	}
	return builder.String()
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// identityMatches reports whether this process already runs as the requested
// identity. Numeric uids and login names both count; a name may carry a domain
// prefix that the contract does not.
func identityMatches(runAs string) bool {
	runAs = strings.TrimSpace(runAs)
	if runAs == "" {
		return false
	}

	current, err := user.Current()
	if err != nil {
		return false
	}
	if runAs == current.Uid || strings.EqualFold(runAs, current.Username) {
		return true
	}
	if index := strings.LastIndex(current.Username, `\`); index >= 0 {
		if runAs == current.Username[index+1:] {
			return true
		}
	}
	return false
}
