package investigation_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/llm"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
)

type scriptedAgent struct {
	decide func(investigation.AgentRequest) (investigation.AgentDecision, error)
	seen   []investigation.AgentRequest
}

func (a *scriptedAgent) Plan(context.Context, string) (investigation.Plan, error) {
	return investigation.Plan{}, errors.New("Plan must not be used in agent mode")
}

func (a *scriptedAgent) Decide(_ context.Context, request investigation.AgentRequest) (investigation.AgentDecision, error) {
	a.seen = append(a.seen, request)
	if a.decide == nil {
		return investigation.AgentDecision{}, errors.New("no scripted decision")
	}
	return a.decide(request)
}

type agentPlanner interface {
	investigation.Planner
	investigation.AgentDecider
}

func newAgentService(t *testing.T, db *store.DB, decider agentPlanner, rounds, calls int) *investigation.Service {
	t.Helper()
	engine, err := policy.New(policy.Config{Version: "test-policy", AllowedRoots: []string{"/var/log"}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	workdir := t.TempDir()
	service, err := investigation.New(db, engine, decider, investigation.Config{
		Bash:               requireBash(t),
		BaseEnv:            map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		WorkDir:            workdir,
		Timeout:            5 * time.Second,
		MaxOutputBytes:     4096,
		MaxCalls:           rounds * calls,
		TotalTimeout:       30 * time.Second,
		AgentLoop:          true,
		AgentRounds:        rounds,
		AgentCallsPerRound: calls,
		AgentContextBytes:  4096,
	})
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	return service
}

func TestAgentLoopChangesDirectionAfterAnObservation(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)
	if _, err := db.Exec(`UPDATE tasks SET request_text = ? WHERE id = ?`, "Java service returns 502", taskID); err != nil {
		t.Fatalf("set request: %v", err)
	}

	decider := &scriptedAgent{}
	decider.decide = func(request investigation.AgentRequest) (investigation.AgentDecision, error) {
		switch len(request.Observations) {
		case 0:
			if !strings.Contains(request.Request, "Java service returns 502") {
				return investigation.AgentDecision{}, fmt.Errorf("agent did not receive the Java task: %q", request.Request)
			}
			return investigation.AgentDecision{
				Kind:     investigation.AgentObserve,
				Commands: []string{"ls /definitely/not/a/java/process"},
			}, nil
		case 1:
			if !strings.Contains(request.Observations[0].Content, "exit_code: 2") {
				return investigation.AgentDecision{}, fmt.Errorf("first observation did not show a missing process: %q", request.Observations[0].Content)
			}
			return investigation.AgentDecision{
				Kind:     investigation.AgentObserve,
				Commands: []string{"printf 'journal: java exited 137'", "printf 'oom-kill: found'"},
			}, nil
		case 3:
			return investigation.AgentDecision{
				Kind:      investigation.AgentFinish,
				Summary:   "the Java process is gone; the journal shows exit 137 and an OOM kill",
				Citations: []int{1, 2, 3},
			}, nil
		default:
			return investigation.AgentDecision{}, fmt.Errorf("unexpected round with %d observations", len(request.Observations))
		}
	}

	service := newAgentService(t, db, decider, 4, 3)
	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusSealed {
		t.Fatalf("expected SEALED, got %s", result.Status)
	}
	if len(result.EvidenceIDs) != 3 {
		t.Fatalf("expected three evidence records, got %v", result.EvidenceIDs)
	}
	if len(decider.seen) != 3 {
		t.Fatalf("expected three model decisions, got %d", len(decider.seen))
	}
	if decider.seen[1].Round != 2 || len(decider.seen[1].Observations) != 1 {
		t.Fatalf("second decision did not receive the first observation: %+v", decider.seen[1])
	}

	var status, summary string
	if err := db.QueryRow(`SELECT status, summary FROM tasks WHERE id = ?`, taskID).Scan(&status, &summary); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status != string(domain.StatusSealed) || !strings.Contains(summary, "OOM kill") {
		t.Fatalf("unexpected task: status=%s summary=%q", status, summary)
	}

	records, err := evidence.New().ListByTask(context.Background(), db, taskID)
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	if len(records) != 3 || records[0].TaskID != taskID || records[2].TaskID != taskID {
		t.Fatalf("evidence is not associated with the task: %+v", records)
	}

	var observed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE task_id = ? AND event_type = 'investigation.observed'`, taskID).Scan(&observed); err != nil {
		t.Fatalf("count observed events: %v", err)
	}
	if observed != 3 {
		t.Fatalf("expected three observation audit events, got %d", observed)
	}
	verified, err := audit.Verify(context.Background(), db)
	if err != nil || !verified.OK {
		t.Fatalf("audit chain: ok=%v err=%v", verified.OK, err)
	}
}

func TestAgentLoopStopsAtTheRoundBudget(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	decider := &scriptedAgent{}
	decider.decide = func(request investigation.AgentRequest) (investigation.AgentDecision, error) {
		return investigation.AgentDecision{
			Kind:     investigation.AgentObserve,
			Commands: []string{fmt.Sprintf("printf 'round-%d'", request.Round)},
		}, nil
	}
	service := newAgentService(t, db, decider, 2, 1)
	if _, err := service.Investigate(context.Background(), taskID); !errors.Is(err, investigation.ErrAgentBudget) {
		t.Fatalf("expected ErrAgentBudget, got %v", err)
	}
}

func TestAgentLoopRejectsDuplicateAndNonReadOnlyCommands(t *testing.T) {
	for name, command := range map[string]string{
		"duplicate":    "printf 'same'",
		"non-readonly": "printf 'x' > /tmp/agent-write",
	} {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			taskID := seedTask(t, db, domain.StatusInvestigating)
			decider := &scriptedAgent{}
			first := true
			decider.decide = func(investigation.AgentRequest) (investigation.AgentDecision, error) {
				if first {
					first = false
					return investigation.AgentDecision{Kind: investigation.AgentObserve, Commands: []string{command}}, nil
				}
				return investigation.AgentDecision{Kind: investigation.AgentObserve, Commands: []string{command}}, nil
			}
			service := newAgentService(t, db, decider, 2, 1)
			if _, err := service.Investigate(context.Background(), taskID); !errors.Is(err, investigation.ErrInvalidPlan) {
				t.Fatalf("expected ErrInvalidPlan, got %v", err)
			}
		})
	}
}

func TestModelPlannerAgentLoopFollowsTheObservation(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	calls := 0
	planner, err := investigation.NewModelPlanner(investigation.ModelConfig{
		Completer: llm.CompleterFunc(func(_ context.Context, request llm.Request) (llm.Response, error) {
			calls++
			switch {
			case !strings.Contains(request.User, "[1] command:"):
				return llm.Response{Content: `{"kind":"observe","commands":["ls /definitely/not/a/java/process"]}`}, nil
			case strings.Contains(request.User, "[3] command:"):
				return llm.Response{Content: `{"kind":"finish","summary":"the Java process is gone and the logs show an OOM kill","citations":[1,2,3]}`}, nil
			case strings.Contains(request.User, "exit_code: 2"):
				return llm.Response{Content: `{"kind":"observe","commands":["printf 'journal: java exited 137'","printf 'oom-kill: found'"]}`}, nil
			default:
				return llm.Response{Content: `{"kind":"observe","commands":["printf 'fallback'"]}`}, nil
			}
		}),
		MaxProbes:           3,
		MaxProbeScriptBytes: 1024,
		MaxObservationBytes: 2048,
		MaxRequestBytes:     1024,
		MaxPromptBytes:      8192,
		MaxTokens:           512,
	})
	if err != nil {
		t.Fatalf("new model planner: %v", err)
	}

	service := newAgentService(t, db, planner, 4, 3)
	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusSealed || len(result.EvidenceIDs) != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if calls != 3 {
		t.Fatalf("expected three model calls, got %d", calls)
	}
}
