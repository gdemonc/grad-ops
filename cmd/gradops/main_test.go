package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/config"
	"github.com/gdemonc/grad-ops/internal/investigation"
)

func TestBuildPlannerWiresTheModelClient(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"probes\":[\"uname -a\"]}"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"{\"summary\":\"ok\",\"citations\":[1],\"action\":null}"}}]}`)
	}))
	defer server.Close()

	planner, err := buildPlanner(config.Config{
		Planner:                "model",
		LLMBaseURL:             server.URL + "/v1",
		LLMModel:               "test-model",
		LLMTimeout:             time.Second,
		LLMMaxResponseBytes:    4096,
		LLMMaxProbes:           3,
		LLMMaxObservationBytes: 1024,
		LLMMaxTokens:           256,
		ExecutorUser:           "gradops-agent",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("build planner: %v", err)
	}

	plan, err := planner.Plan(context.Background(), "check the host")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Probes) != 1 || plan.Probes[0] != "uname -a" {
		t.Fatalf("unexpected plan: %+v", plan)
	}

	concluder, ok := planner.(investigation.Concluder)
	if !ok {
		t.Fatal("the model planner must implement Concluder")
	}
	conclusion, err := concluder.Conclude(context.Background(), "check the host", []investigation.Observation{
		{Index: 1, Command: "uname -a", Content: "Linux"},
	})
	if err != nil {
		t.Fatalf("conclude: %v", err)
	}
	if conclusion.Summary != "ok" || len(conclusion.Citations) != 1 {
		t.Fatalf("unexpected conclusion: %+v", conclusion)
	}
}
