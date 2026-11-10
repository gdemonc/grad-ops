// Package contract freezes exactly what an action will do.
//
// A contract is the object an approval is bound to and the object the executor
// re-derives and compares immediately before running anything. Its digest is
// computed over a canonical encoding, so two encoders — in the same language or
// not — produce the same bytes for the same contract.
package contract

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdemonc/grad-ops/internal/domain"
)

// Version identifies the contract schema. It is part of the digest, so a schema
// change cannot be mistaken for a contract with the same fields.
const Version = "gradops.contract.v1"

// Runtime pins the isolation settings the action was approved under.
type Runtime struct {
	TimeoutMS      int64 `json:"timeout_ms"`
	MaxOutputBytes int64 `json:"max_output_bytes"`
	MaxCalls       int64 `json:"max_calls"`
}

// Postcondition is the independent check that decides whether the action
// achieved what it claimed. It is part of the contract, so the approver sees how
// success will be judged before approving.
type Postcondition struct {
	Command        string `json:"command"`
	ExpectExitCode int    `json:"expect_exit_code"`
	StdoutContains string `json:"stdout_contains"`
}

// Compensation is the action that undoes the main one when verification fails.
// It is part of the contract, so the approver approves the undo along with the
// action rather than being asked for a second decision after a failure.
type Compensation struct {
	Command        string `json:"command"`
	ExpectExitCode int    `json:"expect_exit_code"`
	StdoutContains string `json:"stdout_contains"`
}

// Contract is the immutable description of one side-effecting action.
//
// It deliberately carries no proposal id: the digest must describe the action,
// not the row that happens to hold it, so the same action proposed twice yields
// the same digest.
type Contract struct {
	TaskID        int64             `json:"task_id"`
	Tool          string            `json:"tool"`
	Kind          string            `json:"kind"`
	Script        string            `json:"script"`
	ScriptDigest  string            `json:"script_digest"`
	WorkDir       string            `json:"work_dir"`
	Env           map[string]string `json:"env"`
	Paths         []string          `json:"paths"`
	RunAs         string            `json:"run_as"`
	Reversible    bool              `json:"reversible"`
	PolicyRef     string            `json:"policy_ref"`
	Runtime       Runtime           `json:"runtime"`
	Postcondition Postcondition     `json:"postcondition"`
	Compensation  *Compensation     `json:"compensation,omitempty"`
	Risk          string            `json:"risk"`
	PolicyVersion string            `json:"policy_version"`
	ApprovalLevel string            `json:"approval_level"`
	Nonce         string            `json:"nonce"`
	CreatedAt     string            `json:"created_at"`
	ExpiresAt     string            `json:"expires_at"`
}

// Canonicalize normalizes set-like fields and validates the contract.
//
// It must be called before Digest: a caller that builds the same contract with
// paths in a different order would otherwise get a different digest for the
// same action.
func (c *Contract) Canonicalize() error {
	c.Tool = strings.TrimSpace(c.Tool)
	c.Script = strings.TrimRight(c.Script, "\n")
	c.WorkDir = strings.TrimSpace(c.WorkDir)
	c.RunAs = strings.TrimSpace(c.RunAs)
	c.Nonce = strings.TrimSpace(c.Nonce)
	c.PolicyVersion = strings.TrimSpace(c.PolicyVersion)
	c.ApprovalLevel = strings.TrimSpace(c.ApprovalLevel)

	if c.Tool == "" {
		return fmt.Errorf("contract requires a tool")
	}
	if strings.TrimSpace(c.Kind) == "" {
		return fmt.Errorf("contract requires an action kind")
	}
	c.Kind = strings.TrimSpace(c.Kind)
	if strings.TrimSpace(c.Script) == "" {
		return fmt.Errorf("contract requires a script")
	}
	if c.ScriptDigest == "" {
		c.ScriptDigest = DigestScript(c.Script)
	}
	if !strings.HasPrefix(c.WorkDir, "/") {
		return fmt.Errorf("contract work directory must be an absolute path")
	}
	c.WorkDir = path.Clean(c.WorkDir)
	if c.RunAs == "" {
		return fmt.Errorf("contract requires a run identity")
	}
	if _, err := domain.ParseRiskLevel(c.Risk); err != nil {
		return fmt.Errorf("contract risk: %w", err)
	}
	if c.PolicyVersion == "" {
		return fmt.Errorf("contract requires a policy version")
	}
	if len(c.Nonce) < 16 {
		return fmt.Errorf("contract nonce must be at least 16 characters")
	}
	if c.Runtime.TimeoutMS <= 0 || c.Runtime.MaxOutputBytes <= 0 || c.Runtime.MaxCalls <= 0 {
		return fmt.Errorf("contract runtime limits must all be positive")
	}
	c.Postcondition.Command = strings.TrimSpace(c.Postcondition.Command)
	if c.Postcondition.Command == "" {
		return fmt.Errorf("contract requires a postcondition command")
	}
	if c.Compensation != nil {
		c.Compensation.Command = strings.TrimSpace(c.Compensation.Command)
		if c.Compensation.Command == "" {
			return fmt.Errorf("contract compensation requires a command")
		}
	}

	created, err := domain.ParseTime(c.CreatedAt)
	if err != nil {
		return fmt.Errorf("contract creation time: %w", err)
	}
	expires, err := domain.ParseTime(c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("contract expiry: %w", err)
	}
	if !expires.After(created) {
		return fmt.Errorf("contract must expire after it was created")
	}

	paths := make([]string, 0, len(c.Paths))
	for _, raw := range c.Paths {
		if !strings.HasPrefix(raw, "/") {
			return fmt.Errorf("contract path %q must be absolute", raw)
		}
		paths = append(paths, path.Clean(raw))
	}
	sort.Strings(paths)
	c.Paths = paths

	if c.Env == nil {
		c.Env = map[string]string{}
	}

	return nil
}

// Canonical encodes the contract deterministically.
func (c Contract) Canonical() ([]byte, error) {
	return CanonicalJSON(c)
}

// Digest returns the SHA-256 digest of the canonical encoding.
func (c Contract) Digest() (string, error) {
	canonical, err := c.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// Verify recomputes the digest and compares it with expected.
//
// The comparison is constant time: a digest check must not leak, byte by byte,
// how close a forged contract came.
func Verify(c Contract, expected string) error {
	actual, err := c.Digest()
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return fmt.Errorf("contract digest mismatch: approved %s, derived %s", expected, actual)
	}
	return nil
}

// Expired reports whether the contract's validity window has passed.
func (c Contract) Expired(now time.Time) bool {
	expires, err := domain.ParseTime(c.ExpiresAt)
	if err != nil {
		// An unparsable expiry cannot be used to prove the contract is still
		// valid, so it is treated as expired.
		return true
	}
	return !now.UTC().Before(expires)
}

// DigestScript hashes the script exactly as it will be executed.
func DigestScript(script string) string {
	sum := sha256.Sum256([]byte(script))
	return hex.EncodeToString(sum[:])
}

// CanonicalJSON encodes a value with sorted object keys, no insignificant
// whitespace, no HTML escaping and UTF-8 text.
//
// Floating point numbers are rejected: they have no single canonical form
// across languages, and a contract that cannot be re-encoded identically cannot
// be verified identically.
func CanonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode contract: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var generic any
	if err := decoder.Decode(&generic); err != nil {
		return nil, fmt.Errorf("decode contract for canonicalisation: %w", err)
	}

	var out bytes.Buffer
	if err := writeValue(&out, generic); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeValue(out *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if typed {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case json.Number:
		literal := typed.String()
		if strings.ContainsAny(literal, ".eE") {
			return fmt.Errorf("canonical JSON does not allow floating point numbers (%s)", literal)
		}
		out.WriteString(literal)
	case string:
		return writeString(out, typed)
	case []any:
		out.WriteByte('[')
		for i, item := range typed {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeValue(out, item); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)

		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := writeString(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := writeValue(out, typed[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON cannot encode %T", value)
	}
	return nil
}

func writeString(out *bytes.Buffer, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("canonical JSON requires valid UTF-8")
	}

	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
	return nil
}
