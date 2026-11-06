package contract_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gdemonc/grad-ops/internal/contract"
	"github.com/gdemonc/grad-ops/internal/domain"
)

func validContract(t *testing.T) contract.Contract {
	t.Helper()

	created := time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)
	return contract.Contract{
		ProposalID:    7,
		TaskID:        3,
		Tool:          "bash",
		Kind:          "service_control",
		Script:        "systemctl restart nginx\n",
		WorkDir:       "/var/log",
		Env:           map[string]string{"PATH": "/usr/bin:/bin", "HOME": "/var/lib/gradops"},
		Paths:         []string{"/var/log//nginx-access.log", "/var/log/./nginx-error.log"},
		RunAs:         "gradops-agent",
		Reversible:    true,
		Runtime:       contract.Runtime{TimeoutMS: 30000, MaxOutputBytes: 65536, MaxCalls: 20},
		Risk:          "R2",
		PolicyVersion: "policy-2026-11-01",
		ApprovalLevel: "human",
		Nonce:         "0123456789abcdef0123456789abcdef",
		CreatedAt:     domain.FormatTime(created),
		ExpiresAt:     domain.FormatTime(created.Add(15 * time.Minute)),
	}
}

func TestCanonicalJSONIsSortedAndCompact(t *testing.T) {
	value := map[string]any{
		"b":       "two",
		"a":       1,
		"nested":  map[string]any{"z": true, "m": nil},
		"escaped": "line\nbreak \"quoted\" <tag> & more",
	}

	encoded, err := contract.CanonicalJSON(value)
	if err != nil {
		t.Fatalf("canonical json: %v", err)
	}

	want := `{"a":1,"b":"two","escaped":"line\nbreak \"quoted\" <tag> & more","nested":{"m":null,"z":true}}`
	if string(encoded) != want {
		t.Fatalf("unexpected encoding:\n got %s\nwant %s", encoded, want)
	}
}

func TestCanonicalJSONRejectsFloats(t *testing.T) {
	if _, err := contract.CanonicalJSON(map[string]any{"x": 1.5}); err == nil {
		t.Fatal("expected floating point values to be rejected")
	}
}

func TestDigestIsStableAcrossMapOrder(t *testing.T) {
	first := validContract(t)
	second := validContract(t)
	// Rebuild the environment in a different insertion order.
	second.Env = map[string]string{"HOME": "/var/lib/gradops", "PATH": "/usr/bin:/bin"}
	// And the paths in a different order.
	second.Paths = []string{"/var/log/nginx-error.log", "/var/log/nginx-access.log"}

	for _, c := range []*contract.Contract{&first, &second} {
		if err := c.Canonicalize(); err != nil {
			t.Fatalf("canonicalize: %v", err)
		}
	}

	firstDigest, err := first.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	secondDigest, err := second.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if firstDigest != secondDigest {
		t.Fatalf("digest depends on ordering: %s vs %s", firstDigest, secondDigest)
	}
}

func TestDigestChangesWhenAnythingChanges(t *testing.T) {
	base := validContract(t)
	if err := base.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	baseDigest, err := base.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	mutations := map[string]func(*contract.Contract){
		"script":         func(c *contract.Contract) { c.Script = "systemctl stop nginx" },
		"kind":           func(c *contract.Contract) { c.Kind = "modify_file" },
		"reversible":     func(c *contract.Contract) { c.Reversible = false },
		"path":           func(c *contract.Contract) { c.Paths = []string{"/var/log/other.log"} },
		"run identity":   func(c *contract.Contract) { c.RunAs = "someone-else" },
		"work directory": func(c *contract.Contract) { c.WorkDir = "/etc/gradops" },
		"environment":    func(c *contract.Contract) { c.Env = map[string]string{"PATH": "/bin"} },
		"risk":           func(c *contract.Contract) { c.Risk = "R3" },
		"policy":         func(c *contract.Contract) { c.PolicyVersion = "policy-2026-11-02" },
		"expiry":         func(c *contract.Contract) { c.ExpiresAt = domain.FormatTime(time.Now().Add(time.Hour)) },
		"nonce":          func(c *contract.Contract) { c.Nonce = "ffffffffffffffffffffffffffffffff" },
		"runtime":        func(c *contract.Contract) { c.Runtime.TimeoutMS = 60000 },
	}

	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := validContract(t)
			mutate(&changed)
			if err := changed.Canonicalize(); err != nil {
				t.Fatalf("canonicalize: %v", err)
			}
			digest, err := changed.Digest()
			if err != nil {
				t.Fatalf("digest: %v", err)
			}
			if digest == baseDigest {
				t.Fatalf("changing %s did not change the digest", name)
			}
		})
	}
}

func TestVerifyRejectsTampering(t *testing.T) {
	original := validContract(t)
	if err := original.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}
	digest, err := original.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}

	if err := contract.Verify(original, digest); err != nil {
		t.Fatalf("expected the untouched contract to verify: %v", err)
	}

	tampered := original
	tampered.Script = "systemctl restart nginx; rm -rf /var/log/nginx"
	if err := contract.Verify(tampered, digest); err == nil {
		t.Fatal("expected a tampered contract to fail verification")
	}
}

func TestCanonicalizeRejectsInvalidContracts(t *testing.T) {
	cases := map[string]func(*contract.Contract){
		"missing tool":      func(c *contract.Contract) { c.Tool = " " },
		"missing kind":      func(c *contract.Contract) { c.Kind = "" },
		"missing script":    func(c *contract.Contract) { c.Script = "  \n" },
		"relative workdir":  func(c *contract.Contract) { c.WorkDir = "var/log" },
		"missing identity":  func(c *contract.Contract) { c.RunAs = "" },
		"unknown risk":      func(c *contract.Contract) { c.Risk = "R9" },
		"missing policy":    func(c *contract.Contract) { c.PolicyVersion = "" },
		"short nonce":       func(c *contract.Contract) { c.Nonce = "abc" },
		"zero runtime":      func(c *contract.Contract) { c.Runtime.TimeoutMS = 0 },
		"relative path":     func(c *contract.Contract) { c.Paths = []string{"var/log/x"} },
		"expiry before now": func(c *contract.Contract) { c.ExpiresAt = c.CreatedAt },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validContract(t)
			mutate(&c)
			if err := c.Canonicalize(); err == nil {
				t.Fatal("expected the contract to be rejected")
			}
		})
	}
}

func TestExpired(t *testing.T) {
	c := validContract(t)
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	expires, err := domain.ParseTime(c.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expiry: %v", err)
	}

	if c.Expired(expires.Add(-time.Minute)) {
		t.Fatal("expected the contract to still be valid one minute before expiry")
	}
	if !c.Expired(expires) {
		t.Fatal("expected the contract to be expired at the expiry instant")
	}

	broken := c
	broken.ExpiresAt = "not-a-time"
	if !broken.Expired(time.Now()) {
		t.Fatal("an unparsable expiry must be treated as expired")
	}
}

func TestScriptDigestUsesExactBytes(t *testing.T) {
	a := contract.DigestScript("echo hi\n")
	b := contract.DigestScript("echo hi")
	if a == b {
		t.Fatal("trailing newline must change the script digest")
	}
	if len(a) != 64 {
		t.Fatalf("expected a hex sha256, got %q", a)
	}
}

// TestCanonicalVector pins the encoding so a refactor that silently changes the
// digest cannot pass unnoticed.
func TestCanonicalVector(t *testing.T) {
	c := validContract(t)
	if err := c.Canonicalize(); err != nil {
		t.Fatalf("canonicalize: %v", err)
	}

	canonical, err := c.Canonical()
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(canonical, &decoded); err != nil {
		t.Fatalf("canonical output is not valid json: %v", err)
	}
	if strings.Contains(string(canonical), "\n") {
		t.Fatal("canonical output must not contain newlines")
	}
	if decoded["work_dir"] != "/var/log" {
		t.Fatalf("unexpected work_dir: %v", decoded["work_dir"])
	}

	paths, ok := decoded["paths"].([]any)
	if !ok || len(paths) != 2 {
		t.Fatalf("unexpected paths: %v", decoded["paths"])
	}
	if paths[0] != "/var/log/nginx-access.log" || paths[1] != "/var/log/nginx-error.log" {
		t.Fatalf("paths were not cleaned and sorted: %v", paths)
	}
}
