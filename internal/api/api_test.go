package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gdemonc/grad-ops/internal/api"
	"github.com/gdemonc/grad-ops/internal/store"
	"github.com/gdemonc/grad-ops/internal/task"
)

type taskResponse struct {
	ID          int64   `json:"id"`
	RequestText string  `json:"request_text"`
	Source      string  `json:"source"`
	Status      string  `json:"status"`
	Summary     *string `json:"summary"`
	CreatedAt   string  `json:"created_at"`
}

type timelineEvent struct {
	Seq     int64  `json:"seq"`
	Type    string `json:"type"`
	Actor   string `json:"actor"`
	Message string `json:"message"`
}

type auditVerifyResponse struct {
	OK     bool   `json:"ok"`
	Events int    `json:"events"`
	Reason string `json:"reason"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate database: %v", err)
	}

	handler := api.New(api.Deps{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tasks:   task.NewService(db),
		DB:      db,
		Version: "test",
	})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode request body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}

	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return response.StatusCode, payload
}

func TestHealthEndpoints(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	for _, path := range []string{"/healthz", "/api/health"} {
		status, body := doJSON(t, client, http.MethodGet, server.URL+path, nil)
		if status != http.StatusOK {
			t.Fatalf("%s returned %d: %s", path, status, body)
		}

		var decoded map[string]string
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("%s returned invalid JSON: %v", path, err)
		}
		if decoded["status"] != "ok" {
			t.Fatalf("%s reported status %q", path, decoded["status"])
		}
	}
}

func TestCreateReadListAndAuditFlow(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	status, body := doJSON(t, client, http.MethodPost, server.URL+"/api/tasks",
		map[string]string{"request_text": "检查磁盘使用率异常"})
	if status != http.StatusCreated {
		t.Fatalf("create returned %d: %s", status, body)
	}

	var created taskResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created task: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("expected a positive task id, got %d", created.ID)
	}
	if created.Status != "QUEUED" {
		t.Fatalf("expected a queued task, got %q", created.Status)
	}
	if created.Source != "web" {
		t.Fatalf("expected the default source, got %q", created.Source)
	}

	// Read it back.
	status, body = doJSON(t, client, http.MethodGet,
		server.URL+"/api/tasks/"+itoa(created.ID), nil)
	if status != http.StatusOK {
		t.Fatalf("get returned %d: %s", status, body)
	}
	var fetched taskResponse
	if err := json.Unmarshal(body, &fetched); err != nil {
		t.Fatalf("decode fetched task: %v", err)
	}
	if fetched.ID != created.ID || fetched.RequestText != "检查磁盘使用率异常" {
		t.Fatalf("unexpected task: %+v", fetched)
	}

	// List it.
	status, body = doJSON(t, client, http.MethodGet, server.URL+"/api/tasks", nil)
	if status != http.StatusOK {
		t.Fatalf("list returned %d: %s", status, body)
	}
	var listed []taskResponse
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode task list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 task, got %d", len(listed))
	}

	// The timeline must contain the creation event.
	status, body = doJSON(t, client, http.MethodGet,
		server.URL+"/api/tasks/"+itoa(created.ID)+"/timeline", nil)
	if status != http.StatusOK {
		t.Fatalf("timeline returned %d: %s", status, body)
	}
	var events []timelineEvent
	if err := json.Unmarshal(body, &events); err != nil {
		t.Fatalf("decode timeline: %v", err)
	}
	if len(events) != 1 || events[0].Type != "task.created" {
		t.Fatalf("expected one task.created event, got %+v", events)
	}

	// The chain must verify after real writes.
	status, body = doJSON(t, client, http.MethodGet, server.URL+"/api/audit/verify", nil)
	if status != http.StatusOK {
		t.Fatalf("audit verify returned %d: %s", status, body)
	}
	var verified auditVerifyResponse
	if err := json.Unmarshal(body, &verified); err != nil {
		t.Fatalf("decode audit result: %v", err)
	}
	if !verified.OK || verified.Events != 1 {
		t.Fatalf("expected a valid single-event chain, got %+v", verified)
	}
}

func TestCreateTaskRejectsInvalidInput(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	cases := map[string]struct {
		body        any
		wantMessage string
	}{
		"empty request text": {map[string]string{"request_text": "   "}, "request_text"},
		"unknown field":      {map[string]string{"request_text": "ok", "risk_level": "R0"}, "unknown"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			status, body := doJSON(t, client, http.MethodPost, server.URL+"/api/tasks", tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", status, body)
			}
			var decoded errorResponse
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if decoded.Error.Code != "invalid_request" {
				t.Fatalf("expected invalid_request, got %q", decoded.Error.Code)
			}
			if tc.wantMessage != "" && !bytes.Contains([]byte(decoded.Error.Message), []byte(tc.wantMessage)) {
				t.Fatalf("expected the message to mention %q, got %q", tc.wantMessage, decoded.Error.Message)
			}
		})
	}
}

func TestCreateTaskRejectsMalformedJSON(t *testing.T) {
	server := newTestServer(t)

	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/tasks",
		bytes.NewReader([]byte(`{"request_text":`)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("send request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.StatusCode)
	}
}

func TestMissingTaskIsNotFound(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	for _, path := range []string{"/api/tasks/999", "/api/tasks/999/timeline"} {
		status, body := doJSON(t, client, http.MethodGet, server.URL+path, nil)
		if status != http.StatusNotFound {
			t.Fatalf("%s returned %d: %s", path, status, body)
		}
		var decoded errorResponse
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if decoded.Error.Code != "not_found" {
			t.Fatalf("expected not_found, got %q", decoded.Error.Code)
		}
	}
}

func TestInvalidTaskIDIsRejected(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	for _, path := range []string{"/api/tasks/abc", "/api/tasks/0", "/api/tasks/-1"} {
		status, body := doJSON(t, client, http.MethodGet, server.URL+path, nil)
		if status != http.StatusBadRequest {
			t.Fatalf("%s returned %d: %s", path, status, body)
		}
	}
}

func TestListTasksFiltersByStatus(t *testing.T) {
	server := newTestServer(t)
	client := server.Client()

	if status, body := doJSON(t, client, http.MethodPost, server.URL+"/api/tasks",
		map[string]string{"request_text": "task one"}); status != http.StatusCreated {
		t.Fatalf("create returned %d: %s", status, body)
	}

	status, body := doJSON(t, client, http.MethodGet, server.URL+"/api/tasks?status=QUEUED", nil)
	if status != http.StatusOK {
		t.Fatalf("filtered list returned %d: %s", status, body)
	}
	var listed []taskResponse
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 queued task, got %d", len(listed))
	}

	status, body = doJSON(t, client, http.MethodGet, server.URL+"/api/tasks?status=SEALED", nil)
	if status != http.StatusOK {
		t.Fatalf("filtered list returned %d: %s", status, body)
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed) != 0 {
		t.Fatalf("expected no sealed tasks, got %d", len(listed))
	}

	status, body = doJSON(t, client, http.MethodGet, server.URL+"/api/tasks?status=NOPE", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unknown status, got %d: %s", status, body)
	}
	if status, body = doJSON(t, client, http.MethodGet, server.URL+"/api/tasks?limit=0", nil); status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a zero limit, got %d: %s", status, body)
	}
}

func itoa(id int64) string {
	if id == 0 {
		return "0"
	}
	var digits []byte
	for id > 0 {
		digits = append([]byte{byte('0' + id%10)}, digits...)
		id /= 10
	}
	return string(digits)
}
