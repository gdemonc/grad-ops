package investigation_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/llm"
	"github.com/gdemonc/grad-ops/internal/policy"
)

// TestDeepSeekAgentLoopRealProvider is an opt-in integration test. It makes a
// real network request and therefore must never run with the normal unit
// suite. The key is read from the environment and is never logged.
func TestDeepSeekAgentLoopRealProvider(t *testing.T) {
	if os.Getenv("GRADOPS_TEST_REAL_LLM") != "1" {
		t.Skip("set GRADOPS_TEST_REAL_LLM=1 to run the real provider test")
	}
	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("GRADOPS_LLM_API_KEY"))
	}
	if apiKey == "" {
		t.Fatal("set DEEPSEEK_API_KEY or GRADOPS_LLM_API_KEY (never commit it)")
	}
	baseURL := strings.TrimSpace(os.Getenv("DEEPSEEK_BASE_URL"))
	if baseURL == "" {
		baseURL = "https://api.deepseek.com/v1"
	}
	model := strings.TrimSpace(os.Getenv("DEEPSEEK_MODEL"))
	if model == "" {
		model = "deepseek-chat"
	}

	client, err := llm.NewOpenAI(llm.Config{
		BaseURL:          baseURL,
		APIKey:           apiKey,
		Model:            model,
		Timeout:          90 * time.Second,
		MaxResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("build deepseek client: %v", err)
	}

	workdir := t.TempDir()
	fixture := filepath.Join(workdir, "service.log")
	if err := os.WriteFile(fixture, []byte("ERROR deepseek-fixture: connection refused\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	db := testDB(t)
	taskID := seedTask(t, db, domain.StatusInvestigating)
	request := fmt.Sprintf("Read the local file %s with a read-only command and report the exact error line. Do not modify anything.", fixture)
	if _, err := db.Exec(`UPDATE tasks SET request_text = ? WHERE id = ?`, request, taskID); err != nil {
		t.Fatalf("set request: %v", err)
	}

	engine, err := policy.New(policy.Config{Version: "deepseek-test", AllowedRoots: []string{"/var/log"}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	planner, err := investigation.NewModelPlanner(investigation.ModelConfig{
		Completer:           client,
		MaxProbes:           4,
		MaxProbeScriptBytes: 2048,
		MaxObservationBytes: 4096,
		MaxRequestBytes:     4096,
		MaxPromptBytes:      64 << 10,
		MaxTokens:           1024,
	})
	if err != nil {
		t.Fatalf("new model planner: %v", err)
	}
	service, err := investigation.New(db, engine, planner, investigation.Config{
		Bash:               requireBash(t),
		BaseEnv:            map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		WorkDir:            workdir,
		Timeout:            15 * time.Second,
		MaxOutputBytes:     4096,
		MaxCalls:           12,
		TotalTimeout:       5 * time.Minute,
		AgentLoop:          true,
		AgentRounds:        8,
		AgentMaxCalls:      12,
		AgentCallsPerRound: 3,
		AgentContextBytes:  32 << 10,
	})
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}

	result, err := service.Investigate(context.Background(), taskID)
	if err != nil {
		t.Fatalf("real deepseek investigation: %v", err)
	}
	if result.Status != domain.StatusSealed || len(result.EvidenceIDs) == 0 {
		t.Fatalf("unexpected result: %+v", result)
	}

	records, err := evidence.New().ListByTask(context.Background(), db, taskID)
	if err != nil {
		t.Fatalf("list evidence: %v", err)
	}
	var observedFixture bool
	for _, record := range records {
		if strings.Contains(record.Content, "deepseek-fixture") {
			observedFixture = true
		}
		t.Logf("evidence attempt=%d command=%q content=%q", record.Attempt, record.Command, truncateForLog(record.Content, 400))
	}
	if !observedFixture {
		t.Fatalf("the model never read the fixture file; evidence count=%d", len(records))
	}

	var summary string
	if err := db.QueryRow(`SELECT summary FROM tasks WHERE id = ?`, taskID).Scan(&summary); err != nil {
		t.Fatalf("read summary: %v", err)
	}
	t.Logf("deepseek summary: %s", summary)
	verified, err := audit.Verify(context.Background(), db)
	if err != nil || !verified.OK {
		t.Fatalf("audit chain: ok=%v err=%v", verified.OK, err)
	}
}

func truncateForLog(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}
