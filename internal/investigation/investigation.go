// Package investigation turns a request into evidence, and optionally into a
// proposal.
//
// It is bounded and read-only: every command it runs is a probe, every probe
// becomes evidence, and a conclusion must cite the probes it rests on. A side
// effect is never performed here — at most it is described, priced by policy and
// handed to the approval flow.
package investigation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/shell"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/worker"
)

var (
	// ErrTaskNotFound means no task has that id.
	ErrTaskNotFound = errors.New("task not found")
	// ErrWrongState means the task is not being investigated.
	ErrWrongState = errors.New("task is not under investigation")
	// ErrUngrounded means a conclusion cites evidence that does not exist.
	ErrUngrounded = errors.New("conclusion is not grounded in evidence")
	// ErrInvalidPlan means the planner returned something unusable.
	ErrInvalidPlan = errors.New("invalid investigation plan")
	// ErrInvalidConfig means the service was configured with unusable values.
	ErrInvalidConfig = errors.New("invalid investigation configuration")
)

// Action is a proposed side effect.
type Action struct {
	Kind          policy.Kind
	Script        string
	Paths         []string
	RunAs         string
	Reversible    bool
	Postcondition contract.Postcondition
	Compensation  *contract.Compensation
	Rationale     string
}

// Observation is one probe result as a concluding planner sees it.
type Observation struct {
	Index     int
	Command   string
	Content   string
	Truncated bool
}

// Conclusion is a planner's judgment after it has seen the probes.
//
// Citations are 1-based probe numbers, the same numbering the plan uses. The
// investigation service maps them to stored evidence IDs; a citation that does
// not identify a probe is refused.
type Conclusion struct {
	Summary   string
	Citations []int
	Action    *Action
}

// Concluder is an optional second phase for planners that need to see evidence
// before drawing a conclusion. A planner that implements it first returns the
// probes to run, then receives their bounded observations and returns the
// summary, citations and any proposed action.
type Concluder interface {
	Conclude(ctx context.Context, request string, observations []Observation) (Conclusion, error)
}

// Plan is what a planner asks for.
type Plan struct {
	// Probes are read-only commands. Each becomes one piece of evidence.
	Probes []string
	// Action is the side effect to propose, if any.
	Action *Action
	// Summary explains a conclusion drawn without acting.
	Summary string
	// Citations are 1-based probe numbers the conclusion rests on.
	Citations []int
}

// Planner decides what to look at and what to conclude.
//
// It is an interface so that the rule-based stand-in used today can be replaced
// by a model without changing the machinery around it.
type Planner interface {
	Plan(ctx context.Context, request string) (Plan, error)
}

// ScriptedPlanner returns a fixed plan. It exists to exercise the loop
// deterministically; it reads nothing and decides nothing on its own.
type ScriptedPlanner struct {
	Probes    []string
	Action    *Action
	Summary   string
	Citations []int
}

// Plan implements Planner.
func (p ScriptedPlanner) Plan(context.Context, string) (Plan, error) {
	return Plan{Probes: p.Probes, Action: p.Action, Summary: p.Summary, Citations: p.Citations}, nil
}

// Config is the service's fixed configuration.
type Config struct {
	Bash    string
	BaseEnv map[string]string
	// WorkDir is the host directory probes run in.
	WorkDir string
	// ContractWorkDir is the POSIX path recorded in a contract. It defaults to
	// "/" and differs from WorkDir only on a development host.
	ContractWorkDir string
	Timeout         time.Duration
	MaxOutputBytes  int
	MaxCalls        int
	// ScratchDir is where the persistent console keeps its capture files. Empty
	// means the operating system's temporary directory.
	ScratchDir string
	// PersistentProbes runs every probe of one investigation in the same Bash
	// process, so a probe may rely on what an earlier probe exported or where it
	// changed directory. Off by default: probes are supposed to be independent
	// observations unless the operator asks otherwise.
	PersistentProbes bool
	// RunAs is the identity probes run as. Empty keeps the service identity.
	RunAs string
	// Limits bounds each probe shell process.
	Limits shell.Limits
	// Cgroup is the optional cgroup v2 boundary for probe shells.
	Cgroup shell.CgroupConfig
	// Sandbox is the optional bubblewrap namespace/filesystem boundary for
	// probe shells.
	Sandbox shell.SandboxConfig
	// TotalTimeout bounds one investigation across every probe and model call.
	// Zero uses the default.
	TotalTimeout time.Duration
	// AgentLoop selects the multi-round observe/finish loop. The planner must
	// implement AgentDecider.
	AgentLoop bool
	// AgentRounds bounds the number of model decisions. Zero uses the default.
	AgentRounds int
	// AgentMaxCalls bounds the total observations across all rounds. Zero uses
	// the default.
	AgentMaxCalls int
	// AgentCallsPerRound bounds the observations in one decision. Zero uses the
	// default.
	AgentCallsPerRound int
	// AgentContextBytes bounds the observation context sent to the decider.
	// Zero uses the default.
	AgentContextBytes int
}

// Result describes an investigation.
type Result struct {
	TaskID      int64
	Status      domain.Status
	ProposalID  int64
	EvidenceIDs []string
}

// Service investigates tasks.
type Service struct {
	db       *store.DB
	planner  Planner
	engine   *policy.Engine
	evidence *evidence.Store
	cfg      Config
	now      func() time.Time
}

// New builds a service.
func New(db *store.DB, engine *policy.Engine, planner Planner, cfg Config) (*Service, error) {
	if db == nil || engine == nil || planner == nil {
		return nil, fmt.Errorf("%w: database, policy and planner are required", ErrInvalidConfig)
	}
	if strings.TrimSpace(cfg.Bash) == "" {
		return nil, fmt.Errorf("%w: a shell path is required", ErrInvalidConfig)
	}
	if cfg.Timeout <= 0 || cfg.MaxOutputBytes <= 0 || cfg.MaxCalls <= 0 {
		return nil, fmt.Errorf("%w: runtime limits must be positive", ErrInvalidConfig)
	}
	if cfg.TotalTimeout <= 0 {
		cfg.TotalTimeout = 2 * time.Minute
	}
	if cfg.TotalTimeout > 30*time.Minute {
		return nil, fmt.Errorf("%w: total investigation timeout must not exceed 30m", ErrInvalidConfig)
	}
	if cfg.AgentLoop {
		if _, ok := planner.(AgentDecider); !ok {
			return nil, fmt.Errorf("%w: agent loop requires an AgentDecider", ErrInvalidConfig)
		}
		if cfg.AgentRounds == 0 {
			cfg.AgentRounds = 20
		}
		if cfg.AgentMaxCalls == 0 {
			cfg.AgentMaxCalls = 40
		}
		if cfg.AgentCallsPerRound == 0 {
			cfg.AgentCallsPerRound = 5
		}
		if cfg.AgentContextBytes == 0 {
			cfg.AgentContextBytes = 32 << 10
		}
		if cfg.AgentRounds < 2 || cfg.AgentRounds > 20 {
			return nil, fmt.Errorf("%w: agent rounds must be between 2 and 20", ErrInvalidConfig)
		}
		if cfg.AgentMaxCalls < 1 || cfg.AgentMaxCalls > 200 {
			return nil, fmt.Errorf("%w: agent max calls must be between 1 and 200", ErrInvalidConfig)
		}
		if cfg.AgentCallsPerRound < 1 || cfg.AgentCallsPerRound > 8 || cfg.AgentCallsPerRound > cfg.AgentMaxCalls {
			return nil, fmt.Errorf("%w: agent calls per round must be between 1 and 8 and not exceed max calls", ErrInvalidConfig)
		}
		if cfg.MaxCalls < cfg.AgentMaxCalls {
			return nil, fmt.Errorf("%w: shell max calls must not be less than agent max calls", ErrInvalidConfig)
		}
		if cfg.AgentContextBytes < 1024 || cfg.AgentContextBytes > 1<<20 {
			return nil, fmt.Errorf("%w: agent context bytes must be between 1024 and 1 MiB", ErrInvalidConfig)
		}
	}
	if cfg.ContractWorkDir == "" {
		cfg.ContractWorkDir = "/"
	}
	if !strings.HasPrefix(cfg.ContractWorkDir, "/") {
		return nil, fmt.Errorf("%w: contract work directory must be an absolute POSIX path", ErrInvalidConfig)
	}

	return &Service{
		db:       db,
		planner:  planner,
		engine:   engine,
		evidence: evidence.New(),
		cfg:      cfg,
		now:      time.Now,
	}, nil
}

// Handle implements worker.Handler for investigation jobs.
func (s *Service) Handle(ctx context.Context, job worker.Job) error {
	if job.Kind != domain.JobKindInvestigate {
		return fmt.Errorf("investigation cannot handle job kind %s", job.Kind)
	}
	_, err := s.investigate(ctx, job.TaskID, job.Attempts)
	return err
}

// Investigate runs one investigation outside a worker attempt. Direct callers
// use attempt zero; the worker path tags evidence with job.Attempts.
func (s *Service) Investigate(ctx context.Context, taskID int64) (Result, error) {
	return s.investigate(ctx, taskID, 0)
}

func (s *Service) investigate(ctx context.Context, taskID int64, attempt int) (Result, error) {
	if s.cfg.TotalTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.TotalTimeout)
		defer cancel()
	}

	request, err := s.loadRequest(ctx, taskID)
	if err != nil {
		return Result{}, err
	}

	if s.cfg.AgentLoop {
		return s.investigateAgent(ctx, taskID, request, attempt)
	}

	plan, err := s.planner.Plan(ctx, request)
	if err != nil {
		return Result{}, fmt.Errorf("plan investigation: %w", err)
	}
	if len(plan.Probes) == 0 {
		return Result{}, fmt.Errorf("%w: a plan needs at least one probe", ErrInvalidPlan)
	}

	concluder, twoPhase := s.planner.(Concluder)
	if !twoPhase && plan.Action == nil && strings.TrimSpace(plan.Summary) == "" {
		return Result{}, fmt.Errorf("%w: a plan without an action needs a conclusion", ErrInvalidPlan)
	}
	if err := validateProbes(plan.Probes); err != nil {
		return Result{}, err
	}
	if !twoPhase && plan.Action != nil {
		if err := validateAction(*plan.Action); err != nil {
			return Result{}, err
		}
	}

	observations, err := s.probe(ctx, plan.Probes, attempt)
	if err != nil {
		return Result{}, err
	}
	for i := range observations {
		if observations[i].ID == "" {
			if observations[i].ID, err = evidence.NewID(); err != nil {
				return Result{}, err
			}
		}
	}

	if twoPhase {
		conclusion, err := concluder.Conclude(ctx, request, observationsFrom(plan.Probes, observations))
		if err != nil {
			return Result{}, fmt.Errorf("conclude investigation: %w", err)
		}
		plan.Summary = conclusion.Summary
		plan.Citations = conclusion.Citations
		plan.Action = conclusion.Action
		if plan.Action == nil && strings.TrimSpace(plan.Summary) == "" {
			return Result{}, fmt.Errorf("%w: the conclusion is empty", ErrInvalidPlan)
		}
	}
	if plan.Action != nil {
		if err := validateAction(*plan.Action); err != nil {
			return Result{}, err
		}
	}

	if plan.Action == nil {
		return s.conclude(ctx, taskID, plan, observations)
	}
	return s.propose(ctx, taskID, plan, observations)
}

// observationsFrom presents the probe results with the same 1-based numbering
// the plan uses for citations.
func observationsFrom(probes []string, records []evidence.Record) []Observation {
	out := make([]Observation, 0, len(records))
	for index := range records {
		command := ""
		if index < len(probes) {
			command = probes[index]
		}
		out = append(out, Observation{
			Index:     index + 1,
			Command:   command,
			Content:   records[index].Content,
			Truncated: records[index].Truncated,
		})
	}
	return out
}

func (s *Service) loadRequest(ctx context.Context, taskID int64) (string, error) {
	var (
		request string
		status  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT request_text, status FROM tasks WHERE id = ?`, taskID).Scan(&request, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrTaskNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load task: %w", err)
	}
	if domain.Status(status) != domain.StatusInvestigating {
		return "", fmt.Errorf("%w: task is %s", ErrWrongState, status)
	}
	return request, nil
}

// validateProbes refuses a script that is declared read-only but has an effect
// the analyzer can recognize. The analyzer is not a sandbox; it is a cheap
// second opinion that keeps a planner (or a model) from turning a probe into a
// write without the approval path noticing.
func validateProbes(probes []string) error {
	for index, probe := range probes {
		analysis, err := command.Analyze(probe)
		if err != nil {
			return fmt.Errorf("%w: probe %d cannot be parsed: %v", ErrInvalidPlan, index+1, err)
		}
		if !analysis.ReadOnly() {
			return fmt.Errorf("%w: probe %d is not read-only: %s", ErrInvalidPlan, index+1, analysis.Summary())
		}
	}
	return nil
}

// validateAction enforces the one declaration the analyzer can check cheaply:
// an action declared as observation must not contain a recognizable write,
// indirect execution or dynamic command name. Other kinds are priced by policy
// and, where required, shown to a human.
func validateAction(action Action) error {
	analysis, err := command.Analyze(action.Script)
	if err != nil {
		return fmt.Errorf("%w: proposed script cannot be parsed: %v", ErrInvalidPlan, err)
	}
	if action.Kind == policy.KindObserve && !analysis.ReadOnly() {
		return fmt.Errorf("%w: action is declared observe but %s", ErrInvalidPlan, analysis.Summary())
	}
	return nil
}

// probe runs the plan's commands and returns their recordings.
// probeRunner is the slice of shell.Session and shell.Console the investigation
// needs; both satisfy it.
type probeRunner interface {
	Run(ctx context.Context, script string) (shell.Result, error)
}

func (s *Service) probe(ctx context.Context, probes []string, attempt int) (records []evidence.Record, runErr error) {
	runner, release, err := s.probeShell()
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := release(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	records = make([]evidence.Record, 0, len(probes))
	for _, command := range probes {
		result, err := runner.Run(ctx, command)
		if err != nil {
			return nil, fmt.Errorf("run probe %q: %w", command, err)
		}
		records = append(records, evidence.Record{
			Attempt:    attempt,
			Tool:       "probe",
			Command:    command,
			ObservedAt: s.now().UTC(),
			Content:    render(result),
			Truncated:  result.Truncated(),
		})
	}
	return records, nil
}

// conclude seals a read-only investigation whose conclusion cites its evidence.
func (s *Service) conclude(ctx context.Context, taskID int64, plan Plan, records []evidence.Record) (Result, error) {
	ids, err := s.citations(plan, records)
	if err != nil {
		return Result{}, err
	}

	now := s.now().UTC()
	result := Result{TaskID: taskID, Status: domain.StatusSealed}

	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		for i := range records {
			records[i].TaskID = taskID
			if err := s.evidence.Save(ctx, tx, &records[i]); err != nil {
				return err
			}
			result.EvidenceIDs = append(result.EvidenceIDs, records[i].ID)
		}

		if err := s.evidence.VerifyCitations(ctx, tx, taskID, ids); err != nil {
			return fmt.Errorf("%w: %v", ErrUngrounded, err)
		}

		if err := s.transitionTask(ctx, tx, taskID, domain.StatusSealed, "", plan.Summary, now); err != nil {
			return err
		}

		payload, err := json.Marshal(map[string]any{
			"summary":      plan.Summary,
			"evidence_ids": ids,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "investigator",
			Type:      "investigation.sealed",
			Message:   "investigation concluded without a side effect",
			Payload:   payload,
			CreatedAt: now,
		})
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// propose records the evidence and creates a proposal for the approval flow.
func (s *Service) propose(ctx context.Context, taskID int64, plan Plan, records []evidence.Record) (Result, error) {
	action := plan.Action

	// An action may be proposed without citations (the stub path does), but if
	// the planner did cite probes they must identify evidence saved with this
	// task. This keeps a model's rationale tied to the observations it saw.
	var citedIDs []string
	if len(plan.Citations) > 0 {
		var err error
		if citedIDs, err = s.citations(plan, records); err != nil {
			return Result{}, err
		}
	}

	evaluation := s.engine.Evaluate(policy.Request{
		Subject:    policy.Subject{ID: "investigator"},
		Kind:       action.Kind,
		Command:    action.Script,
		Paths:      action.Paths,
		RunAs:      action.RunAs,
		Reversible: action.Reversible,
	})
	if evaluation.Decision == policy.Deny {
		if err := s.reject(ctx, taskID, plan, records, evaluation); err != nil {
			return Result{}, err
		}
		return Result{TaskID: taskID, Status: domain.StatusRejected}, nil
	}

	now := s.now().UTC()
	nonce, err := newNonce()
	if err != nil {
		return Result{}, err
	}

	c := contract.Contract{
		TaskID:        taskID,
		Tool:          "bash",
		Kind:          string(action.Kind),
		Script:        action.Script,
		WorkDir:       s.cfg.ContractWorkDir,
		Env:           s.cfg.BaseEnv,
		Paths:         action.Paths,
		RunAs:         action.RunAs,
		Reversible:    action.Reversible,
		Runtime:       contract.Runtime{TimeoutMS: s.cfg.Timeout.Milliseconds(), MaxOutputBytes: int64(s.cfg.MaxOutputBytes), MaxCalls: 1},
		Postcondition: action.Postcondition,
		Compensation:  action.Compensation,
		Risk:          string(evaluation.Risk),
		PolicyVersion: evaluation.PolicyVersion,
		ApprovalLevel: approvalLevel(evaluation),
		Nonce:         nonce,
		CreatedAt:     domain.FormatTime(now),
		ExpiresAt:     domain.FormatTime(now.Add(15 * time.Minute)),
	}
	if err := c.Canonicalize(); err != nil {
		return Result{}, fmt.Errorf("build contract: %w", err)
	}
	digest, err := c.Digest()
	if err != nil {
		return Result{}, fmt.Errorf("digest contract: %w", err)
	}
	rawContract, err := json.Marshal(c)
	if err != nil {
		return Result{}, fmt.Errorf("encode contract: %w", err)
	}

	status := "PENDING_APPROVAL"
	if evaluation.Decision == policy.Allow {
		status = "APPROVED"
	}

	rationale := strings.TrimSpace(action.Rationale)
	if rationale == "" {
		rationale = evaluation.Reason
	}

	result := Result{TaskID: taskID, Status: domain.StatusAwaitingApproval}
	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		for i := range records {
			records[i].TaskID = taskID
			if err := s.evidence.Save(ctx, tx, &records[i]); err != nil {
				return err
			}
			result.EvidenceIDs = append(result.EvidenceIDs, records[i].ID)
		}

		insert, err := tx.ExecContext(ctx,
			`INSERT INTO proposals
			    (task_id, tool, arguments, risk_level, policy_version, contract, contract_digest, status, rationale, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, "bash", string(rawContract), string(evaluation.Risk), evaluation.PolicyVersion,
			string(rawContract), digest, status, rationale, domain.FormatTime(now))
		if err != nil {
			return fmt.Errorf("insert proposal: %w", err)
		}
		if result.ProposalID, err = insert.LastInsertId(); err != nil {
			return fmt.Errorf("proposal id: %w", err)
		}

		if len(citedIDs) > 0 {
			if err := s.evidence.VerifyCitations(ctx, tx, taskID, citedIDs); err != nil {
				return fmt.Errorf("%w: %v", ErrUngrounded, err)
			}
		}

		if err := s.transitionTask(ctx, tx, taskID, domain.StatusAwaitingApproval, "", plan.Summary, now); err != nil {
			return err
		}

		payload, err := json.Marshal(map[string]any{
			"proposal_id":     result.ProposalID,
			"contract_digest": digest,
			"risk":            string(evaluation.Risk),
			"kind":            string(action.Kind),
			"requires_human":  evaluation.RequiresApproval,
			"evidence_ids":    result.EvidenceIDs,
			"cited_ids":       citedIDs,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "investigator",
			Type:      "investigation.proposed",
			Message:   fmt.Sprintf("proposed %s action as proposal %d", action.Kind, result.ProposalID),
			Payload:   payload,
			CreatedAt: now,
		})
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// reject records a plan that policy refuses, together with the evidence behind it.
func (s *Service) reject(ctx context.Context, taskID int64, plan Plan, records []evidence.Record, evaluation policy.Result) error {
	now := s.now().UTC()

	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		var ids []string
		for i := range records {
			records[i].TaskID = taskID
			if err := s.evidence.Save(ctx, tx, &records[i]); err != nil {
				return err
			}
			ids = append(ids, records[i].ID)
		}

		if err := s.transitionTask(ctx, tx, taskID, domain.StatusRejected, "policy", plan.Summary, now); err != nil {
			return err
		}

		payload, err := json.Marshal(map[string]any{
			"risk":         string(evaluation.Risk),
			"reason":       evaluation.Reason,
			"matched":      evaluation.MatchedRules,
			"evidence_ids": ids,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "investigator",
			Type:      "investigation.rejected",
			Message:   "policy refused the proposed action",
			Payload:   payload,
			CreatedAt: now,
		})
	})
}

func (s *Service) transitionTask(ctx context.Context, tx *sql.Tx, taskID int64, to domain.Status, stage, summary string, now time.Time) error {
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = ?`, taskID).Scan(&current); err != nil {
		return fmt.Errorf("load task: %w", err)
	}
	if err := domain.ValidateTransition(domain.Status(current), to); err != nil {
		return err
	}

	var failureStage any
	if stage != "" {
		failureStage = stage
	}
	var summaryValue any
	if strings.TrimSpace(summary) != "" {
		summaryValue = summary
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = ?, failure_stage = ?, summary = COALESCE(?, summary), updated_at = ?
		  WHERE id = ?`,
		string(to), failureStage, summaryValue, domain.FormatTime(now), taskID); err != nil {
		return fmt.Errorf("update task: %w", err)
	}
	return nil
}

// citations maps 1-based probe numbers to stored evidence ids.
func (s *Service) citations(plan Plan, records []evidence.Record) ([]string, error) {
	if len(plan.Citations) == 0 {
		return nil, fmt.Errorf("%w: the conclusion cites nothing", ErrUngrounded)
	}

	ids := make([]string, 0, len(plan.Citations))
	for _, index := range plan.Citations {
		if index < 1 || index > len(records) {
			return nil, fmt.Errorf("%w: citation %d is not a probe", ErrUngrounded, index)
		}
		ids = append(ids, records[index-1].ID)
	}
	return ids, nil
}

func approvalLevel(evaluation policy.Result) string {
	if evaluation.RequiresApproval {
		return "human"
	}
	return "auto"
}

func newNonce() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

func render(result shell.Result) string {
	var builder strings.Builder
	builder.WriteString("stdout:\n")
	builder.WriteString(result.Stdout)
	builder.WriteString("\nstderr:\n")
	builder.WriteString(result.Stderr)
	builder.WriteString(fmt.Sprintf("\nexit_code: %d", result.ExitCode))
	if result.TimedOut {
		builder.WriteString("\n[timed out]")
	}
	if result.Truncated() {
		builder.WriteString("\n[output truncated]")
	}
	return builder.String()
}

// probeShell opens either a one-shot session or a persistent console. The
// returned release function is never nil, so a console is closed even when a
// probe fails.
func (s *Service) probeShell() (probeRunner, func() error, error) {
	cfg := shell.Config{
		Executable:     s.cfg.Bash,
		WorkDir:        s.cfg.WorkDir,
		Env:            s.cfg.BaseEnv,
		Timeout:        s.cfg.Timeout,
		MaxOutputBytes: s.cfg.MaxOutputBytes,
		MaxCalls:       s.cfg.MaxCalls,
		ScratchDir:     s.cfg.ScratchDir,
		RunAs:          s.cfg.RunAs,
		Limits:         s.cfg.Limits,
		Cgroup:         s.cfg.Cgroup,
		Sandbox:        s.cfg.Sandbox,
	}

	if s.cfg.PersistentProbes {
		console, err := shell.NewConsole(cfg)
		if err != nil {
			return nil, func() error { return nil }, fmt.Errorf("prepare investigation console: %w", err)
		}
		return console, console.Close, nil
	}

	session, err := shell.NewSession(cfg)
	if err != nil {
		return nil, func() error { return nil }, fmt.Errorf("prepare investigation shell: %w", err)
	}
	return session, func() error { return nil }, nil
}
