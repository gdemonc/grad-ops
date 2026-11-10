package investigation_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/llm"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
)

func newServiceWithTimeout(t *testing.T, db *store.DB, planner investigation.Planner, timeout time.Duration) *investigation.Service {
	t.Helper()
	engine, err := policy.New(policy.Config{Version: "test-policy", AllowedRoots: []string{"/var/log"}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	workdir := t.TempDir()
	service, err := investigation.New(db, engine, planner, investigation.Config{
		Bash:           requireBash(t),
		BaseEnv:        map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		WorkDir:        workdir,
		Timeout:        time.Second,
		MaxOutputBytes: 1024,
		MaxCalls:       2,
		TotalTimeout:   timeout,
	})
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	return service
}

func testModelPlanner(t *testing.T, reply llm.CompleterFunc) *investigation.ModelPlanner {
	t.Helper()
	planner, err := investigation.NewModelPlanner(investigation.ModelConfig{
		Completer:           reply,
		MaxProbes:           4,
		MaxProbeScriptBytes: 1024,
		MaxObservationBytes: 1024,
		MaxRequestBytes:     1024,
		MaxPromptBytes:      8192,
		MaxTokens:           512,
		DefaultRunAs:        "gradops-agent",
	})
	if err != nil {
		t.Fatalf("new model planner: %v", err)
	}
	return planner
}

func TestModelPlannerParsesFencedProbeJSON(t *testing.T) {
	planner := testModelPlanner(t, func(context.Context, llm.Request) (llm.Response, error) {
		return llm.Response{Content: "```json\n{\"probes\":[\"uname -a\",\"df -h\"]}\n```"}, nil
	})

	plan, err := planner.Plan(context.Background(), "check the host")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Probes) != 2 || plan.Probes[0] != "uname -a" || plan.Probes[1] != "df -h" {
		t.Fatalf("unexpected probes: %v", plan.Probes)
	}
	if plan.Action != nil || plan.Summary != "" {
		t.Fatalf("the first phase must only choose probes: %+v", plan)
	}
}

func TestModelPlannerRejectsUnusablePlans(t *testing.T) {
	cases := map[string]string{
		"no json":       "I will run uname",
		"empty":         `{"probes":[]}`,
		"blank probe":   `{"probes":["  "]}`,
		"too many":      `{"probes":["a","b","c","d","e"]}`,
		"probe too big": `{"probes":["` + strings.Repeat("x", 2000) + `"]}`,
	}

	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			planner := testModelPlanner(t, func(context.Context, llm.Request) (llm.Response, error) {
				return llm.Response{Content: reply}, nil
			})
			if _, err := planner.Plan(context.Background(), "request"); !errors.Is(err, investigation.ErrInvalidPlan) {
				t.Fatalf("expected ErrInvalidPlan, got %v", err)
			}
		})
	}
}

func TestModelPlannerConcludeParsesAGroundedAction(t *testing.T) {
	planner := testModelPlanner(t, func(_ context.Context, req llm.Request) (llm.Response, error) {
		if !strings.Contains(req.User, "OBSERVATIONS") || !strings.Contains(req.User, "[1] command: uname -a") {
			t.Fatalf("prompt does not contain the observations: %s", req.User)
		}
		return llm.Response{Content: `{
			"summary": "the service is inactive",
			"citations": [1],
			"action": {
				"kind": "service_control",
				"script": "printf 'restarted'",
				"paths": ["/var/log/app.log"],
				"run_as": "",
				"reversible": true,
				"rationale": "restart the inactive unit",
				"postcondition": {"command": "printf 'active'", "expect_exit_code": 0},
				"compensation": {"command": "printf 'stopped'", "expect_exit_code": 0}
			}
		}`}, nil
	})

	conclusion, err := planner.Conclude(context.Background(), "restart the app", []investigation.Observation{
		{Index: 1, Command: "uname -a", Content: "Linux host"},
	})
	if err != nil {
		t.Fatalf("conclude: %v", err)
	}
	if conclusion.Summary != "the service is inactive" || len(conclusion.Citations) != 1 {
		t.Fatalf("unexpected conclusion: %+v", conclusion)
	}
	if conclusion.Action == nil {
		t.Fatal("expected an action")
	}
	if conclusion.Action.RunAs != "gradops-agent" {
		t.Fatalf("run_as default not applied: %q", conclusion.Action.RunAs)
	}
	if conclusion.Action.Compensation == nil || conclusion.Action.Compensation.Command != "printf 'stopped'" {
		t.Fatalf("compensation not parsed: %+v", conclusion.Action.Compensation)
	}
	if conclusion.Action.Postcondition.Command != "printf 'active'" {
		t.Fatalf("postcondition not parsed: %+v", conclusion.Action.Postcondition)
	}
}

func TestModelPlannerRejectsUnsafeConclusions(t *testing.T) {
	base := `{"summary":"s","citations":[1],"action":%s}`
	cases := map[string]string{
		"unknown kind":       `{"kind":"delete_everything","script":"x","postcondition":{"command":"true"}}`,
		"empty script":       `{"kind":"service_control","script":"","postcondition":{"command":"true"}}`,
		"missing postcond":   `{"kind":"service_control","script":"printf x"}`,
		"root":               `{"kind":"service_control","script":"printf x","run_as":"root","postcondition":{"command":"true"}}`,
		"relative path":      `{"kind":"modify_file","script":"printf x","paths":["relative"],"postcondition":{"command":"true"}}`,
		"blank compensation": `{"kind":"modify_file","script":"printf x","compensation":{"command":""},"postcondition":{"command":"true"}}`,
	}

	for name, action := range cases {
		t.Run(name, func(t *testing.T) {
			planner := testModelPlanner(t, func(context.Context, llm.Request) (llm.Response, error) {
				return llm.Response{Content: strings.Replace(base, "%s", action, 1)}, nil
			})
			if _, err := planner.Conclude(context.Background(), "request", []investigation.Observation{
				{Index: 1, Command: "uname -a", Content: "Linux"},
			}); !errors.Is(err, investigation.ErrInvalidPlan) {
				t.Fatalf("expected ErrInvalidPlan, got %v", err)
			}
		})
	}
}

func TestModelPlannerRejectsBadCitationsAndEmptyConclusion(t *testing.T) {
	cases := map[string]string{
		"citation":               `{"summary":"s","citations":[2],"action":null}`,
		"empty":                  `{"summary":"","citations":[],"action":null}`,
		"action without support": `{"summary":"s","citations":[],"action":{"kind":"service_control","script":"printf x","postcondition":{"command":"true"}}}`,
	}

	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			planner := testModelPlanner(t, func(context.Context, llm.Request) (llm.Response, error) {
				return llm.Response{Content: reply}, nil
			})
			if _, err := planner.Conclude(context.Background(), "request", []investigation.Observation{
				{Index: 1, Command: "uname -a", Content: "Linux"},
			}); !errors.Is(err, investigation.ErrInvalidPlan) {
				t.Fatalf("expected ErrInvalidPlan, got %v", err)
			}
		})
	}
}

func TestModelPlannerCapsObservationsInThePrompt(t *testing.T) {
	var prompt string
	planner := testModelPlanner(t, func(_ context.Context, req llm.Request) (llm.Response, error) {
		prompt = req.User
		return llm.Response{Content: `{"summary":"ok","citations":[1],"action":null}`}, nil
	})

	_, err := planner.Conclude(context.Background(), "request", []investigation.Observation{
		{Index: 1, Command: "cat big", Content: strings.Repeat("x", 5000)},
		{Index: 2, Command: "cat other", Content: strings.Repeat("y", 5000)},
	})
	if err != nil {
		t.Fatalf("conclude: %v", err)
	}
	if !strings.Contains(prompt, "[observation truncated]") {
		t.Fatal("expected the per-observation truncation marker")
	}
	if len(prompt) > 9000 {
		t.Fatalf("prompt grew beyond its bound: %d bytes", len(prompt))
	}
}

func TestModelPlannerDrivesARealInvestigation(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	calls := 0
	planner := testModelPlanner(t, func(_ context.Context, req llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			return llm.Response{Content: `{"probes":["printf 'inactive'"]}`}, nil
		}
		if !strings.Contains(req.User, "inactive") {
			t.Fatalf("the conclusion prompt does not contain the probe output: %s", req.User)
		}
		return llm.Response{Content: `{"summary":"the service is inactive","citations":[1],"action":null}`}, nil
	})

	service := newService(t, db, planner)
	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusSealed {
		t.Fatalf("expected SEALED, got %s", result.Status)
	}
	if calls != 2 {
		t.Fatalf("expected two model calls, got %d", calls)
	}

	var summary string
	if err := db.QueryRow(`SELECT summary FROM tasks WHERE id = ?`, taskID).Scan(&summary); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if summary != "the service is inactive" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestModelPlannerProposalCarriesCompensation(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	calls := 0
	planner := testModelPlanner(t, func(context.Context, llm.Request) (llm.Response, error) {
		calls++
		if calls == 1 {
			return llm.Response{Content: `{"probes":["printf 'inactive'"]}`}, nil
		}
		return llm.Response{Content: `{
			"summary": "restart is needed",
			"citations": [1],
			"action": {
				"kind": "service_control",
				"script": "printf 'restarted'",
				"paths": ["/var/log/app.log"],
				"run_as": "gradops-agent",
				"reversible": true,
				"rationale": "the unit is inactive",
				"postcondition": {"command": "printf 'active'", "expect_exit_code": 0},
				"compensation": {"command": "printf 'undo'", "expect_exit_code": 0}
			}
		}`}, nil
	})

	result, err := newService(t, db, planner).Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	if result.Status != domain.StatusAwaitingApproval || result.ProposalID == 0 {
		t.Fatalf("expected an approval proposal, got %+v", result)
	}

	var raw string
	if err := db.QueryRow(`SELECT contract FROM proposals WHERE id = ?`, result.ProposalID).Scan(&raw); err != nil {
		t.Fatalf("read contract: %v", err)
	}
	for _, want := range []string{`"compensation":{"command":"printf 'undo'","expect_exit_code":0,"stdout_contains":""}`, `"run_as":"gradops-agent"`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("contract does not contain %s: %s", want, raw)
		}
	}
}

func TestInvestigationStopsAtTheTotalTimeout(t *testing.T) {
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)

	planner := testModelPlanner(t, func(ctx context.Context, _ llm.Request) (llm.Response, error) {
		<-ctx.Done()
		return llm.Response{}, ctx.Err()
	})

	service := newServiceWithTimeout(t, db, planner, 50*time.Millisecond)
	started := time.Now()
	_, err := service.Investigate(context.Background(), taskID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the investigation budget to expire, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the investigation did not stop near its budget: %s", elapsed)
	}
}
