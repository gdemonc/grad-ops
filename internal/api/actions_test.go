package api_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/api"
	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
	"github.com/gdemonc/grad-ops/internal/verify"
)

type actionHarness struct {
	server     *httptest.Server
	db         *store.DB
	proposalID int64
	taskID     int64
}

func requireBash(t *testing.T) string {
	t.Helper()

	candidate := os.Getenv("GRADOPS_TEST_BASH")
	if candidate == "" {
		found, err := exec.LookPath("bash")
		if err != nil {
			t.Skip("no bash available; set GRADOPS_TEST_BASH")
		}
		candidate = found
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		t.Fatalf("resolve bash: %v", err)
	}
	probe := exec.Command(absolute, "--noprofile", "--norc", "-c", "printf ok")
	if output, err := probe.CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "ok" {
		t.Skipf("bash at %s is not usable: %v (%s)", absolute, err, output)
	}
	return absolute
}

func newActionHarness(t *testing.T) *actionHarness {
	t.Helper()

	bash := requireBash(t)
	workdir := t.TempDir()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gradops.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	engine, err := policy.New(policy.Config{Version: "api-policy", AllowedRoots: []string{"/var/log"}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	evidenceStore := evidence.New()
	approvals, err := approval.New(db, 5*time.Minute)
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	exec, err := executor.New(db, engine, approvals, evidenceStore, executor.Config{
		Bash:    bash,
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
	}, executor.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	verifier, err := verify.New(db, evidenceStore, verify.Config{
		Bash:    bash,
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		Timeout: 10 * time.Second,
	}, verify.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}

	now := time.Now().UTC()
	c := contract.Contract{
		Tool:          "bash",
		Kind:          "service_control",
		Script:        "printf 'restarted'",
		WorkDir:       "/work",
		Env:           map[string]string{"PATH": os.Getenv("PATH")},
		Paths:         []string{"/var/log/app.log"},
		RunAs:         "gradops-agent",
		Reversible:    true,
		Runtime:       contract.Runtime{TimeoutMS: 5000, MaxOutputBytes: 4096, MaxCalls: 5},
		Postcondition: contract.Postcondition{Command: "printf 'active'", StdoutContains: "active"},
		Risk:          string(domain.RiskR2),
		PolicyVersion: "api-policy",
		ApprovalLevel: "human",
		Nonce:         "0123456789abcdef0123456789abcdef",
		CreatedAt:     domain.FormatTime(now),
		ExpiresAt:     domain.FormatTime(now.Add(15 * time.Minute)),
	}
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest, err := c.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("encode contract: %v", err)
	}

	var taskID, proposalID int64
	err = db.Tx(context.Background(), func(tx *sql.Tx) error {
		stamp := domain.FormatTime(now)
		result, err := tx.Exec(
			`INSERT INTO tasks (request_text, status, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			"restart the app", string(domain.StatusAwaitingApproval), stamp, stamp)
		if err != nil {
			return err
		}
		if taskID, err = result.LastInsertId(); err != nil {
			return err
		}
		result, err = tx.Exec(
			`INSERT INTO proposals
			    (task_id, tool, arguments, risk_level, policy_version, contract, contract_digest, status, rationale, created_at)
			 VALUES (?, 'bash', '{}', 'R2', 'api-policy', ?, ?, 'PENDING_APPROVAL', 'the probe says it is down', ?)`,
			taskID, string(raw), digest, stamp)
		if err != nil {
			return err
		}
		proposalID, err = result.LastInsertId()
		return err
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := api.New(api.Deps{
		Logger:          logger,
		Tasks:           task.NewService(db),
		DB:              db,
		Evidence:        evidenceStore,
		Approvals:       approvals,
		Executor:        exec,
		Verifier:        verifier,
		Version:         "test",
		MaxRequestBytes: 1 << 20,
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	return &actionHarness{server: server, db: db, proposalID: proposalID, taskID: taskID}
}

func (h *actionHarness) do(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	request, err := http.NewRequest(method, h.server.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode response %q: %v", raw, err)
		}
	}
	return response.StatusCode, decoded
}

func TestProposalLifecycleOverHTTP(t *testing.T) {
	h := newActionHarness(t)

	status, proposal := h.do(t, http.MethodGet, "/api/proposals/"+itoa(h.proposalID), nil)
	if status != http.StatusOK {
		t.Fatalf("get proposal: status %d", status)
	}
	if proposal["status"] != "PENDING_APPROVAL" {
		t.Fatalf("unexpected proposal status %v", proposal["status"])
	}
	if proposal["contract_digest"] == "" {
		t.Fatal("expected a contract digest")
	}

	// Unknown fields are rejected on the decision body too.
	status, _ = h.do(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "alice", "decision": "MAYBE",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an invalid decision, got %d", status)
	}

	status, grant := h.do(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "alice", "comment": "approved by test",
	})
	if status != http.StatusCreated {
		t.Fatalf("expected 201 from the approval, got %d", status)
	}
	token, _ := grant["token"].(string)
	if token == "" {
		t.Fatal("expected the one-time token to be returned")
	}

	// A second decision on the same proposal is refused.
	status, _ = h.do(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "bob",
	})
	if status != http.StatusConflict {
		t.Fatalf("expected 409 on a second approval, got %d", status)
	}

	// Execution without a token is refused.
	status, _ = h.do(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/execute", map[string]any{
		"actor": "worker-1",
	})
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 without a token, got %d", status)
	}

	status, outcome := h.do(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/execute", map[string]any{
		"actor": "worker-1", "token": token,
	})
	if status != http.StatusOK {
		t.Fatalf("execute: status %d body %v", status, outcome)
	}
	if outcome["status"] != "SUCCEEDED" {
		t.Fatalf("unexpected outcome %v", outcome["status"])
	}

	status, verification := h.do(t, http.MethodPost, "/api/tasks/"+itoa(h.taskID)+"/verify", nil)
	if status != http.StatusOK {
		t.Fatalf("verify: status %d", status)
	}
	if verification["passed"] != true {
		t.Fatalf("expected the postcondition to hold: %v", verification["failures"])
	}

	status, _ = h.do(t, http.MethodPost, "/api/tasks/"+itoa(h.taskID)+"/verify", nil)
	if status != http.StatusConflict {
		t.Fatalf("expected 409 on verifying a sealed task, got %d", status)
	}

	var status2 string
	if err := h.db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, h.taskID).Scan(&status2); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if status2 != string(domain.StatusSealed) {
		t.Fatalf("expected SEALED, got %s", status2)
	}
}

func TestUnknownProposalIsNotFound(t *testing.T) {
	h := newActionHarness(t)

	status, _ := h.do(t, http.MethodGet, "/api/proposals/9999", nil)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}

	status, _ = h.do(t, http.MethodPost, "/api/proposals/9999/execute", map[string]any{"actor": "worker-1"})
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 on unknown proposal execution, got %d", status)
	}
}
