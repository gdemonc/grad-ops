// Package verify decides whether an executed action achieved its postcondition.
//
// Verification is deliberately not part of the executor: the executor reports
// what ran and what it printed, which is evidence of intent, not of effect. This
// package runs the contract's postcondition command as a fresh read-only
// observation after the fact and judges the result against the expectation the
// approver saw.
package verify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/command"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/shell"
	"github.com/gdemonc/grad-ops/internal/store"
)

var (
	// ErrNotVerifiable means the task is not in a state that can be verified.
	ErrNotVerifiable = errors.New("task is not awaiting verification")
	// ErrNoExecutedProposal means there is nothing to verify.
	ErrNoExecutedProposal = errors.New("task has no executed proposal")
	// ErrInvalidRequest means the caller supplied an unusable request.
	ErrInvalidRequest = errors.New("invalid verification request")
	// ErrUnsafePostcondition means the verification command is not read-only.
	// Verification must observe, not repair, so it is refused before running.
	ErrUnsafePostcondition = errors.New("postcondition is not read-only")
)

// Result describes one verification.
type Result struct {
	TaskID     int64
	ProposalID int64
	Passed     bool
	EvidenceID string
	Command    string
	ExitCode   int
	Stdout     string
	Stderr     string
	Failures   []string
	VerifiedAt time.Time
}

// Config is the verifier's fixed configuration.
type Config struct {
	Bash string
	// BaseEnv is merged under the contract environment.
	BaseEnv map[string]string
	// Timeout bounds the postcondition command.
	Timeout time.Duration
	// MaxOutputBytes bounds each captured stream.
	MaxOutputBytes int
	// RunAs is the identity verification commands run as. It should match the
	// identity the action ran as, otherwise the check may see a different
	// filesystem view.
	RunAs string
	// Limits bounds the verification shell.
	Limits shell.Limits
	// Cgroup is the optional cgroup v2 boundary for the verification shell.
	Cgroup shell.CgroupConfig
	// Sandbox is the optional bubblewrap namespace/filesystem boundary.
	Sandbox shell.SandboxConfig
}

// Option customises a Service.
type Option func(*Service)

// WithClock replaces the clock.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithWorkDirMapper translates a contract path to a host path.
func WithWorkDirMapper(mapper func(string) string) Option {
	return func(s *Service) { s.mapWorkDir = mapper }
}

// Service verifies executed proposals.
type Service struct {
	db         *store.DB
	evidence   *evidence.Store
	bash       string
	baseEnv    map[string]string
	timeout    time.Duration
	maxOutput  int
	runAs      string
	limits     shell.Limits
	cgroup     shell.CgroupConfig
	sandbox    shell.SandboxConfig
	now        func() time.Time
	mapWorkDir func(string) string
}

// New builds a verifier.
func New(db *store.DB, ev *evidence.Store, cfg Config, opts ...Option) (*Service, error) {
	if db == nil || ev == nil {
		return nil, fmt.Errorf("%w: database and evidence store are required", ErrInvalidRequest)
	}
	if strings.TrimSpace(cfg.Bash) == "" {
		return nil, fmt.Errorf("%w: a shell path is required", ErrInvalidRequest)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 64 * 1024
	}

	service := &Service{
		db:         db,
		evidence:   ev,
		bash:       cfg.Bash,
		baseEnv:    cfg.BaseEnv,
		timeout:    cfg.Timeout,
		maxOutput:  cfg.MaxOutputBytes,
		runAs:      strings.TrimSpace(cfg.RunAs),
		limits:     cfg.Limits,
		cgroup:     cfg.Cgroup,
		sandbox:    cfg.Sandbox,
		now:        time.Now,
		mapWorkDir: func(path string) string { return path },
	}
	for _, opt := range opts {
		opt(service)
	}
	return service, nil
}

// Verify runs the postcondition of the task's executed proposal.
func (s *Service) Verify(ctx context.Context, taskID int64) (Result, error) {
	if taskID <= 0 {
		return Result{}, fmt.Errorf("%w: task is required", ErrInvalidRequest)
	}

	status, err := s.taskStatus(ctx, taskID)
	if err != nil {
		return Result{}, err
	}
	if status != domain.StatusVerifying {
		return Result{}, fmt.Errorf("%w: task is %s", ErrNotVerifiable, status)
	}

	proposalID, digest, c, err := s.loadExecutedProposal(ctx, taskID)
	if err != nil {
		return Result{}, err
	}
	if err := contract.Verify(c, digest); err != nil {
		return Result{}, fmt.Errorf("verify contract: %w", err)
	}
	analysis, err := command.Analyze(c.Postcondition.Command)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnsafePostcondition, err)
	}
	if !analysis.ReadOnly() {
		return Result{}, fmt.Errorf("%w: %s", ErrUnsafePostcondition, analysis.Summary())
	}

	session, err := shell.NewSession(shell.Config{
		Executable:     s.bash,
		WorkDir:        s.mapWorkDir(c.WorkDir),
		Env:            s.environment(c),
		Timeout:        s.timeout,
		MaxOutputBytes: s.maxOutput,
		MaxCalls:       1,
		RunAs:          s.runAs,
		Limits:         s.limits,
		Cgroup:         s.cgroup,
		Sandbox:        s.sandbox,
	})
	if err != nil {
		return Result{}, fmt.Errorf("prepare verification shell: %w", err)
	}

	checkedAt := s.now().UTC()
	observation, runErr := session.Run(ctx, c.Postcondition.Command)

	result := Result{
		TaskID:     taskID,
		ProposalID: proposalID,
		Command:    c.Postcondition.Command,
		ExitCode:   observation.ExitCode,
		Stdout:     observation.Stdout,
		Stderr:     observation.Stderr,
		VerifiedAt: checkedAt,
	}
	if runErr != nil {
		result.Failures = append(result.Failures, runErr.Error())
	}
	result.Failures = append(result.Failures, evaluate(c.Postcondition, observation, runErr)...)
	result.Passed = len(result.Failures) == 0 && !observation.TimedOut

	record := evidence.Record{
		TaskID:     taskID,
		Tool:       "verify",
		Command:    c.Postcondition.Command,
		ObservedAt: checkedAt,
		Content:    render(observation),
		Truncated:  observation.Truncated(),
	}
	if err := s.record(ctx, taskID, proposalID, c, result, &record); err != nil {
		return result, err
	}
	result.EvidenceID = record.ID
	return result, nil
}

func (s *Service) taskStatus(ctx context.Context, taskID int64) (domain.Status, error) {
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("task %d: %w", taskID, domain.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("load task: %w", err)
	}
	return domain.Status(raw), nil
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
		return 0, "", contract.Contract{}, fmt.Errorf("task %d: %w", taskID, ErrNoExecutedProposal)
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

func (s *Service) environment(c contract.Contract) map[string]string {
	env := make(map[string]string, len(s.baseEnv)+len(c.Env))
	for key, value := range s.baseEnv {
		env[key] = value
	}
	for key, value := range c.Env {
		env[key] = value
	}
	return env
}

func (s *Service) record(ctx context.Context, taskID, proposalID int64, c contract.Contract, result Result, record *evidence.Record) error {
	taskStatus := domain.StatusSealed
	failureStage := ""
	if !result.Passed {
		// A failed action with a defined compensation goes to ROLLING_BACK so
		// the undo can run; without one there is nothing to try, and the task
		// fails outright instead of pretending recovery is available.
		if c.Compensation != nil {
			taskStatus = domain.StatusRollingBack
		} else {
			taskStatus = domain.StatusFailed
		}
		failureStage = "verification"
	}

	passed := 0
	if result.Passed {
		passed = 1
	}

	postcondition := c.Postcondition
	description := strings.TrimSpace(postcondition.StdoutContains)
	if description == "" {
		description = fmt.Sprintf("exit code %d", postcondition.ExpectExitCode)
	}

	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.evidence.Save(ctx, tx, record); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO verification_records (task_id, proposal_id, postcondition, passed, evidence_id, checked_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			taskID, proposalID, description, passed, record.ID, domain.FormatTime(result.VerifiedAt)); err != nil {
			return fmt.Errorf("insert verification: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status = ?, failure_stage = ?, updated_at = ?
			  WHERE id = ? AND status = ?`,
			string(taskStatus), nullable(failureStage), domain.FormatTime(result.VerifiedAt),
			taskID, string(domain.StatusVerifying)); err != nil {
			return fmt.Errorf("update task: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"proposal_id": proposalID,
			"passed":      result.Passed,
			"evidence_id": record.ID,
			"exit_code":   result.ExitCode,
			"failures":    result.Failures,
			"checked_at":  domain.FormatTime(result.VerifiedAt),
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}

		eventType := "verification.passed"
		message := fmt.Sprintf("postcondition of proposal %d held", proposalID)
		if !result.Passed {
			eventType = "verification.failed"
			message = fmt.Sprintf("postcondition of proposal %d did not hold", proposalID)
		}

		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "verifier",
			Type:      eventType,
			Message:   message,
			Payload:   payload,
			CreatedAt: result.VerifiedAt,
		})
	})
}

func evaluate(postcondition contract.Postcondition, observation shell.Result, runErr error) []string {
	if runErr != nil {
		return nil
	}

	var failures []string
	if observation.TimedOut {
		failures = append(failures, "the postcondition check timed out")
	}
	if observation.ExitCode != postcondition.ExpectExitCode {
		failures = append(failures, fmt.Sprintf("expected exit code %d, got %d",
			postcondition.ExpectExitCode, observation.ExitCode))
	}
	if postcondition.StdoutContains != "" && !strings.Contains(observation.Stdout, postcondition.StdoutContains) {
		failures = append(failures, fmt.Sprintf("expected stdout to contain %q", postcondition.StdoutContains))
	}
	return failures
}

func render(observation shell.Result) string {
	var builder strings.Builder
	builder.WriteString("stdout:\n")
	builder.WriteString(observation.Stdout)
	builder.WriteString("\nstderr:\n")
	builder.WriteString(observation.Stderr)
	if observation.Truncated() {
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
