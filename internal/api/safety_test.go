package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gdemonc/grad-ops/internal/api"
)

func safetySummaryFor(t *testing.T, runtime api.RuntimeInfo) string {
	t.Helper()
	server := httptest.NewServer(api.New(api.Deps{Runtime: runtime}))
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/api/safety")
	if err != nil {
		t.Fatalf("safety request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("safety status = %d", response.StatusCode)
	}
	var decoded struct {
		Summary string `json:"summary"`
	}
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode safety: %v", err)
	}
	return decoded.Summary
}

func TestSafetySummaryMatchesTheEnabledRuntime(t *testing.T) {
	both := safetySummaryFor(t, api.RuntimeInfo{RuntimeMode: "cgroup-v2", SandboxMode: "bubblewrap"})
	if !strings.Contains(both, "cgroup v2") || !strings.Contains(both, "bubblewrap") {
		t.Fatalf("summary does not mention the enabled boundaries: %q", both)
	}
	if strings.Contains(both, "均未启用") {
		t.Fatalf("summary contradicts the enabled boundaries: %q", both)
	}

	none := safetySummaryFor(t, api.RuntimeInfo{})
	if !strings.Contains(none, "均未启用") {
		t.Fatalf("summary should admit that isolation is absent: %q", none)
	}
}
