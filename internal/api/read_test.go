package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

func (h *actionHarness) raw(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
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

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return response.StatusCode, payload
}

func TestApprovalQueueReflectsTheCredentialState(t *testing.T) {
	h := newActionHarness(t)

	status, payload := h.raw(t, http.MethodGet, "/api/approvals", nil)
	if status != http.StatusOK {
		t.Fatalf("list approvals: status %d", status)
	}
	var empty []map[string]any
	if err := json.Unmarshal(payload, &empty); err != nil {
		t.Fatalf("decode empty list: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no approvals yet, got %d", len(empty))
	}

	status, body := h.raw(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "alice",
	})
	if status != http.StatusCreated {
		t.Fatalf("approve: status %d body %s", status, body)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatalf("decode grant: %v", err)
	}

	status, payload = h.raw(t, http.MethodGet, "/api/approvals?status=PENDING", nil)
	if status != http.StatusOK {
		t.Fatalf("list pending approvals: status %d", status)
	}
	var pending []struct {
		ID          int64  `json:"id"`
		ProposalID  int64  `json:"proposal_id"`
		TaskID      int64  `json:"task_id"`
		Status      string `json:"status"`
		Tool        string `json:"tool"`
		RiskLevel   string `json:"risk_level"`
		RequestText string `json:"request_text"`
		Approver    string `json:"approver"`
	}
	if err := json.Unmarshal(payload, &pending); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending approval, got %d", len(pending))
	}
	item := pending[0]
	if item.ProposalID != h.proposalID || item.TaskID != h.taskID {
		t.Fatalf("approval points at the wrong rows: %+v", item)
	}
	if item.Status != "PENDING" || item.Approver != "alice" || item.RiskLevel != "R2" {
		t.Fatalf("unexpected approval item: %+v", item)
	}

	if status, body := h.raw(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/execute", map[string]any{
		"actor": "worker-1", "token": grant.Token,
	}); status != http.StatusOK {
		t.Fatalf("execute: status %d body %s", status, body)
	}

	status, payload = h.raw(t, http.MethodGet, "/api/approvals?status=CONSUMED", nil)
	if status != http.StatusOK {
		t.Fatalf("list consumed approvals: status %d", status)
	}
	var consumed []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(payload, &consumed); err != nil {
		t.Fatalf("decode consumed: %v", err)
	}
	if len(consumed) != 1 || consumed[0].Status != "CONSUMED" {
		t.Fatalf("expected the used token to show as CONSUMED, got %+v", consumed)
	}

	if status, payload = h.raw(t, http.MethodGet, "/api/approvals?status=PENDING", nil); status != http.StatusOK {
		t.Fatalf("list pending after use: status %d", status)
	}
	if err := json.Unmarshal(payload, &pending); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a used token must leave the queue, got %d", len(pending))
	}
}

func TestRejectionClosesTheTask(t *testing.T) {
	h := newActionHarness(t)

	status, body := h.raw(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "alice", "comment": "not on my shift", "decision": "REJECTED",
	})
	if status != http.StatusCreated {
		t.Fatalf("reject: status %d body %s", status, body)
	}

	var taskStatus, proposalStatus string
	if err := h.db.QueryRow(`SELECT status FROM tasks WHERE id = ?`, h.taskID).Scan(&taskStatus); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if taskStatus != "REJECTED" {
		t.Fatalf("expected the rejected task to be closed, got %s", taskStatus)
	}
	if err := h.db.QueryRow(`SELECT status FROM proposals WHERE id = ?`, h.proposalID).Scan(&proposalStatus); err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if proposalStatus != "REJECTED" {
		t.Fatalf("expected the proposal to be REJECTED, got %s", proposalStatus)
	}

	// A rejected proposal leaves the queue and cannot be approved afterwards.
	status, payload := h.raw(t, http.MethodGet, "/api/approvals?status=PENDING", nil)
	if status != http.StatusOK {
		t.Fatalf("list pending: status %d", status)
	}
	var pending []map[string]any
	if err := json.Unmarshal(payload, &pending); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a rejected proposal must leave the queue, got %d", len(pending))
	}

	status, _ = h.raw(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "bob",
	})
	if status != http.StatusConflict {
		t.Fatalf("expected a later approval to be refused, got %d", status)
	}
}
func TestTaskReadModels(t *testing.T) {
	h := newActionHarness(t)

	status, payload := h.raw(t, http.MethodGet, "/api/proposals?task_id="+itoa(h.taskID), nil)
	if status != http.StatusOK {
		t.Fatalf("list proposals: status %d", status)
	}
	var proposals []struct {
		ID             int64           `json:"id"`
		TaskID         int64           `json:"task_id"`
		Status         string          `json:"status"`
		ContractDigest string          `json:"contract_digest"`
		Contract       json.RawMessage `json:"contract"`
	}
	if err := json.Unmarshal(payload, &proposals); err != nil {
		t.Fatalf("decode proposals: %v", err)
	}
	if len(proposals) != 1 {
		t.Fatalf("expected one proposal, got %d", len(proposals))
	}
	if proposals[0].ID != h.proposalID || len(proposals[0].Contract) == 0 {
		t.Fatalf("unexpected proposal: %+v", proposals[0])
	}

	status, payload = h.raw(t, http.MethodGet, "/api/tasks/"+itoa(h.taskID)+"/executions", nil)
	if status != http.StatusOK {
		t.Fatalf("list executions: status %d", status)
	}
	var executions []map[string]any
	if err := json.Unmarshal(payload, &executions); err != nil {
		t.Fatalf("decode executions: %v", err)
	}
	if len(executions) != 0 {
		t.Fatalf("expected no executions before running the action, got %d", len(executions))
	}

	status, body := h.raw(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/approvals", map[string]any{
		"approver": "alice",
	})
	if status != http.StatusCreated {
		t.Fatalf("approve: status %d body %s", status, body)
	}
	var grant struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &grant); err != nil {
		t.Fatalf("decode grant: %v", err)
	}

	if status, body := h.raw(t, http.MethodPost, "/api/proposals/"+itoa(h.proposalID)+"/execute", map[string]any{
		"actor": "worker-1", "token": grant.Token,
	}); status != http.StatusOK {
		t.Fatalf("execute: status %d body %s", status, body)
	}
	if status, body := h.raw(t, http.MethodPost, "/api/tasks/"+itoa(h.taskID)+"/verify", nil); status != http.StatusOK {
		t.Fatalf("verify: status %d body %s", status, body)
	}

	status, payload = h.raw(t, http.MethodGet, "/api/tasks/"+itoa(h.taskID)+"/executions", nil)
	if err := json.Unmarshal(payload, &executions); err != nil {
		t.Fatalf("decode executions: %v", err)
	}
	if len(executions) != 1 || executions[0]["outcome"] != "SUCCEEDED" {
		t.Fatalf("expected one SUCCEEDED execution, got %+v", executions)
	}

	status, payload = h.raw(t, http.MethodGet, "/api/tasks/"+itoa(h.taskID)+"/verifications", nil)
	if status != http.StatusOK {
		t.Fatalf("list verifications: status %d", status)
	}
	var verifications []struct {
		Passed     bool   `json:"passed"`
		EvidenceID string `json:"evidence_id"`
	}
	if err := json.Unmarshal(payload, &verifications); err != nil {
		t.Fatalf("decode verifications: %v", err)
	}
	if len(verifications) != 1 || !verifications[0].Passed || verifications[0].EvidenceID == "" {
		t.Fatalf("expected one passing verification, got %+v", verifications)
	}
}

func TestSafetyReportAdmitsTheMissingRuntime(t *testing.T) {
	h := newActionHarness(t)

	status, payload := h.raw(t, http.MethodGet, "/api/safety", nil)
	if status != http.StatusOK {
		t.Fatalf("safety: status %d", status)
	}

	var report struct {
		OverallStatus string `json:"overall_status"`
		Guards        []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
		} `json:"guards"`
	}
	if err := json.Unmarshal(payload, &report); err != nil {
		t.Fatalf("decode safety: %v", err)
	}

	byKey := map[string]string{}
	for _, guard := range report.Guards {
		byKey[guard.Key] = guard.Status
	}
	if byKey["isolation"] != "warn" {
		t.Fatalf("runtime isolation must not be reported as ok, got %q", byKey["isolation"])
	}
	if byKey["identity"] != "warn" {
		t.Fatalf("identity switching must not be reported as ok, got %q", byKey["identity"])
	}
	if report.OverallStatus != "warn" {
		t.Fatalf("expected the overall status to be warn while isolation is missing, got %q", report.OverallStatus)
	}
}
