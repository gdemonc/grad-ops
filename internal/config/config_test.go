package config_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/config"
)

// loadWith sets the minimum a valid configuration needs, then the caller's
// overrides. Load validates the shell and the work directory, so the defaults
// that only hold on Linux are replaced with paths this host really has.
func loadWith(t *testing.T, overrides map[string]string) (config.Config, error) {
	t.Helper()

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("executable: %v", err)
	}

	t.Setenv("GRADOPS_BASH", self)
	t.Setenv("GRADOPS_WORK_DIR", t.TempDir())
	t.Setenv("GRADOPS_DB_PATH", t.TempDir()+string(os.PathSeparator)+"config.db")
	t.Setenv("GRADOPS_ADDR", "127.0.0.1:0")
	for key, value := range overrides {
		t.Setenv(key, value)
	}
	return config.Load()
}

func TestRequireRunAsMatchDefaultsOff(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RequireRunAsMatch {
		t.Fatalf("enforcement must be off by default: the default deployment cannot switch user")
	}
}

func TestRequireRunAsMatchAcceptsBooleans(t *testing.T) {
	for raw, want := range map[string]bool{
		"true": true, "1": true, "on": true, "TRUE": true,
		"false": false, "0": false, "off": false,
	} {
		cfg, err := loadWith(t, map[string]string{"GRADOPS_REQUIRE_RUN_AS_MATCH": raw})
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if cfg.RequireRunAsMatch != want {
			t.Fatalf("%s: got %t, want %t", raw, cfg.RequireRunAsMatch, want)
		}
	}
}

func TestRequireRunAsMatchRejectsJunk(t *testing.T) {
	_, err := loadWith(t, map[string]string{"GRADOPS_REQUIRE_RUN_AS_MATCH": "maybe"})
	if err == nil {
		t.Fatalf("expected an error for an unusable boolean")
	}
	if !strings.Contains(err.Error(), "GRADOPS_REQUIRE_RUN_AS_MATCH") {
		t.Fatalf("error should name the variable, got %v", err)
	}
}

func TestResourceLimitsHaveSafeDefaults(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Limits.IsZero() {
		t.Fatal("resource limits must be on by default")
	}
	if cfg.Limits.CPU <= 0 || cfg.Limits.AddressSpaceBytes <= 0 ||
		cfg.Limits.MaxProcesses <= 0 || cfg.Limits.MaxFileSizeBytes <= 0 ||
		cfg.Limits.MaxOpenFiles <= 0 {
		t.Fatalf("incomplete default limits: %+v", cfg.Limits)
	}
	if cfg.Prlimit == "" {
		t.Fatal("a default prlimit path is required when limits are enabled")
	}
}

func TestResourceLimitsCanBeDisabledIndividually(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		"GRADOPS_RLIMIT_CPU":         "0",
		"GRADOPS_RLIMIT_AS_BYTES":    "0",
		"GRADOPS_RLIMIT_NPROC":       "0",
		"GRADOPS_RLIMIT_FSIZE_BYTES": "0",
		"GRADOPS_RLIMIT_NOFILE":      "0",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Limits.IsZero() {
		t.Fatalf("expected all limits disabled, got %+v", cfg.Limits)
	}
}

func TestResourceLimitsRejectNegativeValues(t *testing.T) {
	_, err := loadWith(t, map[string]string{"GRADOPS_RLIMIT_AS_BYTES": "-1"})
	if err == nil {
		t.Fatal("expected an error for a negative limit")
	}
	if !strings.Contains(err.Error(), "GRADOPS_RLIMIT_AS_BYTES") {
		t.Fatalf("error should name the variable, got %v", err)
	}
}

func TestExecutorUserRejectsRoot(t *testing.T) {
	for _, raw := range []string{"root", "ROOT", "0"} {
		_, err := loadWith(t, map[string]string{"GRADOPS_EXECUTOR_USER": raw})
		if err == nil {
			t.Fatalf("%s: expected root to be rejected", raw)
		}
	}
}

func TestPlannerDefaultsToStub(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Planner != "stub" {
		t.Fatalf("planner = %q, want stub", cfg.Planner)
	}
}

func TestModelPlannerRequiresProviderSettings(t *testing.T) {
	_, err := loadWith(t, map[string]string{"GRADOPS_PLANNER": "model"})
	if err == nil {
		t.Fatal("expected an error without a base URL and model")
	}
}

func TestPlannerRejectsUnknownMode(t *testing.T) {
	if _, err := loadWith(t, map[string]string{"GRADOPS_PLANNER": "magic"}); err == nil {
		t.Fatal("expected an error for an unknown planner")
	}
}

func TestModelPlannerAcceptsProviderSettings(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		"GRADOPS_PLANNER":        "model",
		"GRADOPS_LLM_BASE_URL":   "https://example.com/v1",
		"GRADOPS_LLM_MODEL":      "test-model",
		"GRADOPS_LLM_MAX_PROBES": "3",
		"GRADOPS_LLM_MAX_TOKENS": "256",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Planner != "model" || cfg.LLMMaxProbes != 3 || cfg.LLMMaxTokens != 256 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestInvestigationTimeoutDefaultsAndParses(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.InvestigationTimeout != 5*time.Minute {
		t.Fatalf("default investigation timeout = %s", cfg.InvestigationTimeout)
	}

	cfg, err = loadWith(t, map[string]string{"GRADOPS_INVESTIGATION_TIMEOUT": "90s"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.InvestigationTimeout != 90*time.Second {
		t.Fatalf("investigation timeout = %s", cfg.InvestigationTimeout)
	}

	if _, err := loadWith(t, map[string]string{"GRADOPS_INVESTIGATION_TIMEOUT": "0"}); err == nil {
		t.Fatal("expected zero to be rejected")
	}
}

func TestNonLoopbackRequiresAnAPIToken(t *testing.T) {
	_, err := loadWith(t, map[string]string{"GRADOPS_ADDR": "0.0.0.0:8080"})
	if err == nil {
		t.Fatal("expected a non-loopback listener without a token to be rejected")
	}
	if !strings.Contains(err.Error(), "GRADOPS_API_TOKEN") {
		t.Fatalf("error should name the missing token variable, got %v", err)
	}
}

func TestAPITokenAllowsNonLoopback(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		"GRADOPS_ADDR":      "0.0.0.0:8080",
		"GRADOPS_API_TOKEN": "0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.APIToken != "0123456789abcdef" {
		t.Fatalf("token = %q", cfg.APIToken)
	}
}

func TestShortAPITokenIsRejected(t *testing.T) {
	_, err := loadWith(t, map[string]string{"GRADOPS_API_TOKEN": "short"})
	if err == nil {
		t.Fatal("expected a short token to be rejected")
	}
}

func TestLocalhostHostnameIsAllowedWithoutAToken(t *testing.T) {
	if _, err := loadWith(t, map[string]string{"GRADOPS_ADDR": "localhost:8080"}); err != nil {
		t.Fatalf("localhost should count as loopback: %v", err)
	}
}

func TestExecutionTimeoutDefaultsAndParses(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ExecutionTimeout != 5*time.Minute {
		t.Fatalf("default execution timeout = %s", cfg.ExecutionTimeout)
	}

	cfg, err = loadWith(t, map[string]string{"GRADOPS_EXECUTION_TIMEOUT": "45s"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.ExecutionTimeout != 45*time.Second {
		t.Fatalf("execution timeout = %s", cfg.ExecutionTimeout)
	}

	if _, err := loadWith(t, map[string]string{"GRADOPS_EXECUTION_TIMEOUT": "0"}); err == nil {
		t.Fatal("expected zero to be rejected")
	}
}

func TestRuntimeModeDefaultsToDirect(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RuntimeMode != "direct" || cfg.Cgroup.Enabled {
		t.Fatalf("runtime = %q cgroup enabled = %t", cfg.RuntimeMode, cfg.Cgroup.Enabled)
	}
}

func TestCgroupRuntimeParsesLimits(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		"GRADOPS_RUNTIME":                  "cgroup-v2",
		"GRADOPS_CGROUP_MEMORY_MAX_BYTES":  "268435456",
		"GRADOPS_CGROUP_PIDS_MAX":          "32",
		"GRADOPS_CGROUP_CPU_QUOTA_MICROS":  "50000",
		"GRADOPS_CGROUP_CPU_PERIOD_MICROS": "100000",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RuntimeMode != "cgroup-v2" || !cfg.Cgroup.Enabled {
		t.Fatalf("runtime = %q cgroup enabled = %t", cfg.RuntimeMode, cfg.Cgroup.Enabled)
	}
	if cfg.Cgroup.MemoryMaxBytes != 268435456 || cfg.Cgroup.PidsMax != 32 ||
		cfg.Cgroup.CPUQuotaMicros != 50000 || cfg.Cgroup.CPUPeriodMicros != 100000 {
		t.Fatalf("unexpected cgroup config: %+v", cfg.Cgroup)
	}
}

func TestRuntimeModeRejectsUnknownValues(t *testing.T) {
	if _, err := loadWith(t, map[string]string{"GRADOPS_RUNTIME": "namespace"}); err == nil {
		t.Fatal("expected an unknown runtime mode to be rejected")
	}
}

func TestCgroupLimitsRejectNegativeValues(t *testing.T) {
	if _, err := loadWith(t, map[string]string{"GRADOPS_CGROUP_MEMORY_MAX_BYTES": "-1"}); err == nil {
		t.Fatal("expected a negative cgroup limit to be rejected")
	}
}

func TestSandboxDefaultsToNoneAndParsesOptions(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SandboxMode != "none" || cfg.SandboxNetwork {
		t.Fatalf("sandbox mode = %q network = %t", cfg.SandboxMode, cfg.SandboxNetwork)
	}

	cfg, err = loadWith(t, map[string]string{
		"GRADOPS_SANDBOX":         "bubblewrap",
		"GRADOPS_BWRAP":           "/usr/bin/bwrap",
		"GRADOPS_SANDBOX_NETWORK": "true",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SandboxMode != "bubblewrap" || !cfg.SandboxNetwork {
		t.Fatalf("sandbox mode = %q network = %t", cfg.SandboxMode, cfg.SandboxNetwork)
	}
}

func TestSandboxRejectsUnknownModeAndRelativeBinary(t *testing.T) {
	if _, err := loadWith(t, map[string]string{"GRADOPS_SANDBOX": "namespace"}); err == nil {
		t.Fatal("expected an unknown sandbox mode to be rejected")
	}
	if _, err := loadWith(t, map[string]string{
		"GRADOPS_SANDBOX": "bubblewrap",
		"GRADOPS_BWRAP":   "bwrap",
	}); err == nil {
		t.Fatal("expected a relative bubblewrap path to be rejected")
	}
}

func TestAgentLoopDefaultsOffAndParsesBounds(t *testing.T) {
	cfg, err := loadWith(t, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AgentLoop || cfg.AgentRounds != 20 || cfg.AgentMaxCalls != 40 || cfg.AgentCallsPerRound != 5 || cfg.AgentContextBytes != 32<<10 {
		t.Fatalf("unexpected agent defaults: loop=%t rounds=%d max_calls=%d calls=%d context=%d",
			cfg.AgentLoop, cfg.AgentRounds, cfg.AgentMaxCalls, cfg.AgentCallsPerRound, cfg.AgentContextBytes)
	}

	cfg, err = loadWith(t, map[string]string{
		"GRADOPS_PLANNER":                   "model",
		"GRADOPS_LLM_BASE_URL":              "http://127.0.0.1:18080/v1",
		"GRADOPS_LLM_MODEL":                 "test-model",
		"GRADOPS_AGENT_LOOP":                "true",
		"GRADOPS_AGENT_MAX_ROUNDS":          "4",
		"GRADOPS_AGENT_MAX_CALLS":           "8",
		"GRADOPS_AGENT_MAX_CALLS_PER_ROUND": "2",
		"GRADOPS_AGENT_CONTEXT_BYTES":       "8192",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.AgentLoop || cfg.AgentRounds != 4 || cfg.AgentMaxCalls != 8 || cfg.AgentCallsPerRound != 2 || cfg.AgentContextBytes != 8192 {
		t.Fatalf("unexpected agent config: loop=%t rounds=%d max_calls=%d calls=%d context=%d",
			cfg.AgentLoop, cfg.AgentRounds, cfg.AgentMaxCalls, cfg.AgentCallsPerRound, cfg.AgentContextBytes)
	}
}

func TestAgentLoopRequiresModelPlannerAndValidBounds(t *testing.T) {
	if _, err := loadWith(t, map[string]string{"GRADOPS_AGENT_LOOP": "true"}); err == nil {
		t.Fatal("expected agent loop with the stub planner to be rejected")
	}
	for key, value := range map[string]string{
		"GRADOPS_AGENT_MAX_ROUNDS":          "1",
		"GRADOPS_AGENT_MAX_CALLS":           "0",
		"GRADOPS_AGENT_MAX_CALLS_PER_ROUND": "9",
		"GRADOPS_AGENT_CONTEXT_BYTES":       "512",
	} {
		if _, err := loadWith(t, map[string]string{key: value}); err == nil {
			t.Fatalf("expected %s=%s to be rejected", key, value)
		}
	}
}

func TestDeepSeekPresetFillsTheCompatibleEndpoint(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		"GRADOPS_PLANNER":      "model",
		"GRADOPS_LLM_PROVIDER": "deepseek",
		"GRADOPS_LLM_API_KEY":  "test-key",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLMProvider != "deepseek" || cfg.LLMBaseURL != "https://api.deepseek.com" || cfg.LLMModel != "deepseek-flash" {
		t.Fatalf("unexpected deepseek preset: provider=%q base=%q model=%q", cfg.LLMProvider, cfg.LLMBaseURL, cfg.LLMModel)
	}
}

func TestDeepSeekRequiresAnAPIKeyAndKnownProvider(t *testing.T) {
	if _, err := loadWith(t, map[string]string{
		"GRADOPS_PLANNER":      "model",
		"GRADOPS_LLM_PROVIDER": "deepseek",
	}); err == nil {
		t.Fatal("expected deepseek without an API key to be rejected")
	}
	if _, err := loadWith(t, map[string]string{"GRADOPS_LLM_PROVIDER": "unknown"}); err == nil {
		t.Fatal("expected an unknown provider to be rejected")
	}
}
