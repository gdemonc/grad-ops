package config_test

import (
	"os"
	"strings"
	"testing"

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
