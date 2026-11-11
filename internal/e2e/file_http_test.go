package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/api"
	"github.com/gdemonc/grad-ops/internal/approval"
	"github.com/gdemonc/grad-ops/internal/audit"
	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
	"github.com/gdemonc/grad-ops/internal/evidence"
	"github.com/gdemonc/grad-ops/internal/executor"
	"github.com/gdemonc/grad-ops/internal/investigation"
	"github.com/gdemonc/grad-ops/internal/policy"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
	"github.com/gdemonc/grad-ops/internal/worker"
)

func httpJSON(t *testing.T, client *http.Client, method, url string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, payload
}

func TestHTTPFileAppendThroughWorkerApprovalAndExecutor(t *testing.T) {
	bash := requireBash(t)
	root := t.TempDir()
	workdir := t.TempDir()
	targetPath := filepath.Join(root, "app.log")
	if err := os.WriteFile(targetPath, []byte("start\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "gradops.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	engine, err := policy.New(policy.Config{Version: "http-file-policy", AllowedRoots: []string{root}})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	planner := investigation.ScriptedPlanner{
		Probes: []string{"printf 'ready'"},
		Action: &investigation.Action{
			Kind:       policy.KindModifyFile,
			File:       &investigation.FileAction{Root: root, Relative: "app.log", Data: "next\n"},
			RunAs:      current.Username,
			Reversible: true,
			Postcondition: contract.Postcondition{
				Command: "printf ok",
			},
		},
	}
	investigator, err := investigation.New(db, engine, planner, investigation.Config{
		Bash:           bash,
		BaseEnv:        map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
		WorkDir:        workdir,
		Timeout:        10 * time.Second,
		MaxOutputBytes: 4096,
		MaxCalls:       2,
		FileRoots:      []string{root},
	})
	if err != nil {
		t.Fatalf("investigation: %v", err)
	}
	approvals, err := approval.New(db, 5*time.Minute)
	if err != nil {
		t.Fatalf("approvals: %v", err)
	}
	exec, err := executor.New(db, engine, approvals, evidence.New(), executor.Config{
		Bash:    bash,
		BaseEnv: map[string]string{"PATH": os.Getenv("PATH"), "HOME": workdir},
	}, executor.WithWorkDirMapper(func(string) string { return workdir }))
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	tasks := task.NewService(db)
	server := httptest.NewServer(api.New(api.Deps{
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tasks:     tasks,
		DB:        db,
		Evidence:  evidence.New(),
		Approvals: approvals,
		Executor:  exec,
		Verifier:  nil,
		APIToken:  "",
		Version:   "e2e",
	}))
	defer server.Close()

	wk, err := worker.New(db, worker.Config{
		Owner: "http-file-worker", Lease: time.Minute,
		PollInterval: 20 * time.Millisecond, RetryDelay: 20 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("worker: %v", err)
	}
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
	go func() { _ = wk.Run(runCtx, investigator) }()

	status, body := httpJSON(t, server.Client(), http.MethodPost, server.URL+"/api/tasks", map[string]string{
		"request_text": "append a marker to the test log", "source": "e2e",
	})
	if status != http.StatusCreated {
		t.Fatalf("create task: status=%d body=%s", status, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode task: %v", err)
	}

	waitFor(t, 20*time.Second, func() bool {
		var taskStatus, jobStatus string
		if err := db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, created.ID).Scan(&taskStatus); err != nil {
			return false
		}
		if taskStatus != string(domain.StatusAwaitingApproval) {
			return false
		}
		if err := db.QueryRow(`SELECT status FROM task_jobs WHERE task_id = ? ORDER BY id DESC LIMIT 1`, created.ID).Scan(&jobStatus); err != nil {
			return false
		}
		return jobStatus == string(domain.JobStatusDone)
	})
	stopRun()

	var proposalID int64
	var raw, digest string
	if err := db.QueryRow(`SELECT id, contract, contract_digest FROM proposals WHERE task_id = ? ORDER BY id DESC LIMIT 1`, created.ID).
		Scan(&proposalID, &raw, &digest); err != nil {
		t.Fatalf("load proposal: %v", err)
	}
	var frozen contract.Contract
	if err := json.Unmarshal([]byte(raw), &frozen); err != nil {
		t.Fatalf("decode contract: %v", err)
	}
	if err := contract.Verify(frozen, digest); err != nil {
		t.Fatalf("contract verify: %v", err)
	}
	if frozen.FileTarget == nil || frozen.FileTarget.Device == 0 || frozen.FileTarget.Inode == 0 {
		t.Fatalf("contract did not bind a file identity: %+v", frozen.FileTarget)
	}

	status, body = httpJSON(t, server.Client(), http.MethodPost,
		server.URL+"/api/proposals/"+itoaE2E(proposalID)+"/approvals",
		map[string]string{"approver": "alice", "decision": "APPROVED"})
	if status != http.StatusCreated {
		t.Fatalf("approve: status=%d body=%s", status, body)
	}
	var approvalResponse struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &approvalResponse); err != nil || approvalResponse.Token == "" {
		t.Fatalf("approval token missing: %v %s", err, body)
	}

	status, body = httpJSON(t, server.Client(), http.MethodPost,
		server.URL+"/api/proposals/"+itoaE2E(proposalID)+"/execute",
		map[string]string{"actor": "worker-1", "token": approvalResponse.Token})
	if status != http.StatusOK {
		t.Fatalf("execute: status=%d body=%s", status, body)
	}
	var executed struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &executed); err != nil {
		t.Fatalf("decode execution: %v", err)
	}
	if executed.Status != executor.StatusSucceeded {
		t.Fatalf("expected SUCCEEDED, got %s (%s)", executed.Status, body)
	}

	content, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(content) != "start\nnext\n" {
		t.Fatalf("unexpected file content: %q", content)
	}

	status, body = httpJSON(t, server.Client(), http.MethodGet,
		server.URL+"/api/tasks/"+itoaE2E(created.ID)+"/evidence", nil)
	if status != http.StatusOK {
		t.Fatalf("evidence: status=%d body=%s", status, body)
	}
	var evidenceItems []struct {
		Tool    string `json:"tool"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(body, &evidenceItems); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if len(evidenceItems) < 2 || evidenceItems[len(evidenceItems)-1].Tool != "fileops" {
		t.Fatalf("fileops evidence missing: %+v", evidenceItems)
	}
	verified, err := audit.Verify(context.Background(), db)
	if err != nil || !verified.OK {
		t.Fatalf("audit chain: ok=%v err=%v", verified.OK, err)
	}
}

func itoaE2E(id int64) string { return strconv.FormatInt(id, 10) }
