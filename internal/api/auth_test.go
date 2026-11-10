package api_test

import (
	"context"
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

func newAuthenticatedServer(t *testing.T, token string) *httptest.Server {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	server := httptest.NewServer(api.New(api.Deps{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Tasks:    task.NewService(db),
		DB:       db,
		Version:  "test",
		APIToken: token,
	}))
	t.Cleanup(server.Close)
	return server
}

func doRequest(t *testing.T, client *http.Client, method, url, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

func TestAPIHealthChecksDoNotRequireAToken(t *testing.T) {
	server := newAuthenticatedServer(t, "0123456789abcdef")

	for _, path := range []string{"/healthz", "/api/health"} {
		response := doRequest(t, server.Client(), http.MethodGet, server.URL+path, "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, response.StatusCode)
		}
	}
}

func TestAPIRequiresTheConfiguredBearerToken(t *testing.T) {
	const token = "0123456789abcdef"
	server := newAuthenticatedServer(t, token)

	cases := map[string]struct {
		token string
		want  int
	}{
		"missing": {"", http.StatusUnauthorized},
		"wrong":   {"fedcba9876543210", http.StatusUnauthorized},
		"correct": {token, http.StatusOK},
		"prefix":  {token[:8], http.StatusUnauthorized},
	}

	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			response := doRequest(t, server.Client(), http.MethodGet, server.URL+"/api/tasks", test.token)
			if response.StatusCode != test.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.want)
			}
			if test.want == http.StatusUnauthorized && response.Header.Get("WWW-Authenticate") == "" {
				t.Fatal("a 401 must advertise the bearer challenge")
			}
		})
	}
}

func TestAPIWithoutATokenRemainsOpen(t *testing.T) {
	server := newAuthenticatedServer(t, "")
	response := doRequest(t, server.Client(), http.MethodGet, server.URL+"/api/tasks", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}
