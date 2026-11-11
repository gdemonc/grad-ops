package investigation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/shell"
)

// ErrAgentBudget means the loop stopped because its round/call budget was
// exhausted before the model returned a grounded conclusion.
var ErrAgentBudget = errors.New("agent investigation budget exhausted")

// AgentDecisionKind is the only two things the loop accepts from a decider.
type AgentDecisionKind string

const (
	// AgentObserve asks for one or more read-only observations.
	AgentObserve AgentDecisionKind = "observe"
	// AgentFinish ends the investigation with a grounded conclusion.
	AgentFinish AgentDecisionKind = "finish"
)

// AgentDecision is one round of the minimal observe/finish protocol.
type AgentDecision struct {
	Kind      AgentDecisionKind
	Commands  []string
	Summary   string
	Citations []int
}

// AgentRequest is the bounded context presented to a decider.
type AgentRequest struct {
	Request   string
	Round     int
	MaxRounds int
	// RemainingCalls is the number of shell observations still allowed.
	RemainingCalls int
	// MaxCallsPerRound bounds Commands in one observe decision.
	MaxCallsPerRound int
	// Observations are the most recent observations that fit the context
	// budget. Omitted counts older observations that were dropped.
	Observations []Observation
	Omitted      int
}

// AgentDecider decides the next step from the observations so far. It must not
// execute anything; the Service validates and runs the returned commands.
type AgentDecider interface {
	Decide(ctx context.Context, request AgentRequest) (AgentDecision, error)
}

// investigateAgent runs the multi-round read-only loop.
//
// Evidence is saved and audited after every individual observation, not only
// after the final conclusion. If the loop fails midway the task stays in
// INVESTIGATING; the worker decides whether a read-only retry is appropriate.
func (s *Service) investigateAgent(ctx context.Context, taskID int64, request string) (result Result, runErr error) {
	decider, ok := s.planner.(AgentDecider)
	if !ok {
		return Result{}, fmt.Errorf("%w: planner does not implement AgentDecider", ErrInvalidConfig)
	}

	runner, release, err := s.probeShell()
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if closeErr := release(); closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
	}()

	var (
		observations []Observation
		records      []evidence.Record
		seen         = make(map[string]bool)
		calls        int
	)
	for round := 1; round <= s.cfg.AgentRounds; round++ {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if calls >= s.cfg.MaxCalls {
			return Result{}, fmt.Errorf("%w: call budget used before round %d", ErrAgentBudget, round)
		}

		history, omitted := s.boundedAgentObservations(observations)
		decision, err := decider.Decide(ctx, AgentRequest{
			Request:          request,
			Round:            round,
			MaxRounds:        s.cfg.AgentRounds,
			RemainingCalls:   s.cfg.MaxCalls - calls,
			MaxCallsPerRound: s.cfg.AgentCallsPerRound,
			Observations:     history,
			Omitted:          omitted,
		})
		if err != nil {
			return Result{}, fmt.Errorf("agent round %d: %w", round, err)
		}

		switch decision.Kind {
		case AgentObserve:
			if len(decision.Commands) == 0 {
				return Result{}, fmt.Errorf("%w: observe returned no commands", ErrInvalidPlan)
			}
			if len(decision.Commands) > s.cfg.AgentCallsPerRound {
				return Result{}, fmt.Errorf("%w: observe returned %d commands, limit is %d", ErrInvalidPlan, len(decision.Commands), s.cfg.AgentCallsPerRound)
			}
			if calls+len(decision.Commands) > s.cfg.MaxCalls {
				return Result{}, fmt.Errorf("%w: %d commands exceed the remaining %d calls", ErrAgentBudget, len(decision.Commands), s.cfg.MaxCalls-calls)
			}
			if err := validateProbes(decision.Commands); err != nil {
				return Result{}, err
			}

			for _, command := range decision.Commands {
				if seen[command] {
					return Result{}, fmt.Errorf("%w: duplicate observation %q", ErrInvalidPlan, command)
				}
				observed, err := runner.Run(ctx, command)
				if err != nil {
					return Result{}, fmt.Errorf("run observation %q: %w", command, err)
				}
				record := evidence.Record{
					TaskID:     taskID,
					Tool:       "probe",
					Command:    command,
					ObservedAt: s.now().UTC(),
					Content:    render(observed),
					Truncated:  observed.Truncated(),
				}
				if err := s.saveAgentObservation(ctx, taskID, &record, round, calls+1, observed); err != nil {
					return Result{}, err
				}
				records = append(records, record)
				observations = append(observations, Observation{
					Index:     len(observations) + 1,
					Command:   command,
					Content:   record.Content,
					Truncated: record.Truncated,
				})
				seen[command] = true
				calls++
			}

		case AgentFinish:
			summary := strings.TrimSpace(decision.Summary)
			if summary == "" {
				return Result{}, fmt.Errorf("%w: finish returned an empty summary", ErrInvalidPlan)
			}
			if len(summary) > 8<<10 {
				return Result{}, fmt.Errorf("%w: summary exceeds 8 KiB", ErrInvalidPlan)
			}
			if len(decision.Citations) == 0 {
				return Result{}, fmt.Errorf("%w: finish must cite at least one observation", ErrUngrounded)
			}
			return s.finishAgent(ctx, taskID, summary, decision.Citations, records, round, calls)

		default:
			return Result{}, fmt.Errorf("%w: unknown agent decision %q", ErrInvalidPlan, decision.Kind)
		}
	}

	return Result{}, fmt.Errorf("%w: no finish after %d rounds", ErrAgentBudget, s.cfg.AgentRounds)
}

// boundedAgentObservations keeps the newest observations that fit the context
// budget, preserving their original 1-based indexes. The newest observation is
// included even when it alone exceeds the budget, with its content truncated.
func (s *Service) boundedAgentObservations(all []Observation) ([]Observation, int) {
	limit := s.cfg.AgentContextBytes
	if limit <= 0 || len(all) == 0 {
		return all, 0
	}

	out := make([]Observation, 0, len(all))
	used := 0
	start := len(all)
	for index := len(all) - 1; index >= 0; index-- {
		observation := all[index]
		size := len(observation.Command) + len(observation.Content) + 64
		if used+size > limit {
			if len(out) == 0 {
				// The newest observation must be visible; truncate its content
				// rather than hiding the reason the next decision is needed.
				room := limit - len(observation.Command) - 64
				if room < 0 {
					room = 0
				}
				observation.Content = truncateUTF8(observation.Content, room)
				observation.Truncated = true
				out = append(out, observation)
				start = index
			}
			break
		}
		out = append(out, observation)
		used += size
		start = index
	}

	// Reverse into chronological order.
	for left, right := 0, len(out)-1; left < right; left, right = left+1, right-1 {
		out[left], out[right] = out[right], out[left]
	}
	return out, start
}

func truncateUTF8(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func (s *Service) saveAgentObservation(ctx context.Context, taskID int64, record *evidence.Record, round, call int, result shell.Result) error {
	now := s.now().UTC()
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.evidence.Save(ctx, tx, record); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"round":       round,
			"call":        call,
			"evidence_id": record.ID,
			"command":     record.Command,
			"exit_code":   result.ExitCode,
			"timed_out":   result.TimedOut,
			"truncated":   record.Truncated,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "investigator",
			Type:      "investigation.observed",
			Message:   "agent observed the host",
			Payload:   payload,
			CreatedAt: now,
		})
	})
}

func (s *Service) finishAgent(ctx context.Context, taskID int64, summary string, citations []int, records []evidence.Record, rounds, calls int) (Result, error) {
	ids, err := s.citations(Plan{Citations: citations}, records)
	if err != nil {
		return Result{}, err
	}

	now := s.now().UTC()
	result := Result{TaskID: taskID, Status: domain.StatusSealed, EvidenceIDs: make([]string, 0, len(records))}
	for _, record := range records {
		result.EvidenceIDs = append(result.EvidenceIDs, record.ID)
	}

	err = s.db.Tx(ctx, func(tx *sql.Tx) error {
		if err := s.evidence.VerifyCitations(ctx, tx, taskID, ids); err != nil {
			return fmt.Errorf("%w: %v", ErrUngrounded, err)
		}
		if err := s.transitionTask(ctx, tx, taskID, domain.StatusSealed, "", summary, now); err != nil {
			return err
		}
		payload, err := json.Marshal(map[string]any{
			"summary":      summary,
			"evidence_ids": ids,
			"rounds":       rounds,
			"calls":        calls,
		})
		if err != nil {
			return fmt.Errorf("encode audit payload: %w", err)
		}
		return audit.Append(ctx, tx, &audit.Event{
			TaskID:    &taskID,
			Actor:     "investigator",
			Type:      "investigation.sealed",
			Message:   "agent concluded without a side effect",
			Payload:   payload,
			CreatedAt: now,
		})
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}
