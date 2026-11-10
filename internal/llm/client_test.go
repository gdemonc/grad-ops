package llm_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/llm"
)

func newClient(t *testing.T, baseURL, key string) *llm.OpenAI {
	t.Helper()
	client, err := llm.NewOpenAI(llm.Config{
		BaseURL:          baseURL,
		APIKey:           key,
		Model:            "test-model",
		Timeout:          2 * time.Second,
		MaxResponseBytes: 4096,
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func TestCompleteSendsAnOpenAICompatibleRequest(t *testing.T) {
	var (
		gotPath   string
		gotAuth   string
		gotBody   map[string]any
		gotMethod string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model": "test-model",
			"choices": [{"message": {"role": "assistant", "content": " hello "}}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}
		}`)
	}))
	defer server.Close()

	client := newClient(t, server.URL+"/v1", "secret-key")
	response, err := client.Complete(context.Background(), llm.Request{
		System:    "system prompt",
		User:      "user prompt",
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer secret-key" {
		t.Fatalf("authorization = %q", gotAuth)
	}
	if gotBody["model"] != "test-model" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 2 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	if response.Content != "hello" {
		t.Fatalf("content = %q, want hello", response.Content)
	}
	if response.Usage.TotalTokens != 4 {
		t.Fatalf("usage = %+v", response.Usage)
	}
}

func TestCompleteRejectsOversizedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 5000))
	}))
	defer server.Close()

	client := newClient(t, server.URL, "")
	_, err := client.Complete(context.Background(), llm.Request{User: "hello"})
	if !errors.Is(err, llm.ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}
}

func TestCompleteReportsProviderErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
	}))
	defer server.Close()

	client := newClient(t, server.URL, "not-the-key")
	_, err := client.Complete(context.Background(), llm.Request{User: "hello"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error should name the status and provider message, got %v", err)
	}
	if strings.Contains(err.Error(), "not-the-key") {
		t.Fatalf("error leaked the API key: %v", err)
	}
}

func TestCompleteRejectsMalformedAndEmptyResponses(t *testing.T) {
	cases := map[string]string{
		"malformed": "not json",
		"empty":     `{"choices":[]}`,
		"blank":     `{"choices":[{"message":{"content":"  "}}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			client := newClient(t, server.URL, "")
			_, err := client.Complete(context.Background(), llm.Request{User: "hello"})
			if name == "malformed" {
				if err == nil {
					t.Fatal("expected a decode error")
				}
				return
			}
			if !errors.Is(err, llm.ErrEmptyResponse) {
				t.Fatalf("expected ErrEmptyResponse, got %v", err)
			}
		})
	}
}

func TestCompleteHonoursTheContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer server.Close()

	client := newClient(t, server.URL, "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Complete(ctx, llm.Request{User: "hello"})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > time.Second {
		t.Fatalf("the request did not respect the context deadline: %s", time.Since(start))
	}
}

func TestNewOpenAIRejectsUnsafeConfiguration(t *testing.T) {
	cases := map[string]llm.Config{
		"remote plain http": {
			BaseURL: "http://example.com/v1", Model: "m", Timeout: time.Second, MaxResponseBytes: 10,
		},
		"credentials in URL": {
			BaseURL: "https://user:pass@example.com/v1", Model: "m", Timeout: time.Second, MaxResponseBytes: 10,
		},
		"query": {
			BaseURL: "https://example.com/v1?x=1", Model: "m", Timeout: time.Second, MaxResponseBytes: 10,
		},
		"missing model": {
			BaseURL: "https://example.com/v1", Timeout: time.Second, MaxResponseBytes: 10,
		},
		"missing timeout": {
			BaseURL: "https://example.com/v1", Model: "m", MaxResponseBytes: 10,
		},
		"missing limit": {
			BaseURL: "https://example.com/v1", Model: "m", Timeout: time.Second,
		},
	}

	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := llm.NewOpenAI(cfg); !errors.Is(err, llm.ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestNewOpenAIAllowsLoopbackHTTP(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:8080/v1", "http://localhost:8080", "http://[::1]:8080"} {
		if _, err := llm.NewOpenAI(llm.Config{
			BaseURL: base, Model: "m", Timeout: time.Second, MaxResponseBytes: 10,
		}); err != nil {
			t.Fatalf("%s: %v", base, err)
		}
	}
}

func TestCompleteDoesNotFollowProviderRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the client followed a redirect to another endpoint")
	}))
	defer target.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, target.URL, http.StatusFound)
	}))
	defer server.Close()

	client := newClient(t, server.URL, "secret")
	if _, err := client.Complete(context.Background(), llm.Request{User: "hello"}); err == nil {
		t.Fatal("expected the redirect to be refused")
	}
}
