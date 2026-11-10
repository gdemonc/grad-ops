// Package policy decides whether a proposed action may run.
//
// The engine is deterministic and self-contained: it never imports the LLM, the
// API or the executor, and it never reads a risk level supplied by a model.
// Risk is derived from the action's declared kind, its target resources, the
// identity it would run as, and the deployment configuration. Anything the
// engine cannot classify is denied.
package policy

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/gdemonc/grad-ops/internal/domain"
)

// Kind is the declared effect of an action.
type Kind string

const (
	// KindObserve reads state and changes nothing.
	KindObserve Kind = "observe"
	// KindModifyFile writes a file that already exists.
	KindModifyFile Kind = "modify_file"
	// KindServiceControl starts, stops or restarts a service.
	KindServiceControl Kind = "service_control"
	// KindPackageManage installs, removes or upgrades packages.
	KindPackageManage Kind = "package_manage"
	// KindPrivilegeChange changes users, groups or permissions.
	KindPrivilegeChange Kind = "privilege_change"
)

// Decision is the outcome of an evaluation.
type Decision string

const (
	// Allow runs the action without human involvement.
	Allow Decision = "ALLOW"
	// RequiresApproval needs a human decision bound to the exact action.
	RequiresApproval Decision = "REQUIRES_APPROVAL"
	// Deny refuses the action outright.
	Deny Decision = "DENY"
)

// Subject is who asked for the action.
type Subject struct {
	ID   string
	Role string
}

// Request is a proposed action, described by what it does rather than by how
// risky someone claims it is.
type Request struct {
	Subject Subject
	Kind    Kind
	// Command is the shell script that would run.
	Command string
	// Paths are the resources the action declares it touches, including
	// redirection targets. Paths are POSIX absolute paths.
	Paths []string
	// RunAs is the identity the action would be executed as.
	RunAs string
	// Reversible reports whether the action can be undone by a defined
	// compensating action.
	Reversible bool
	// PolicyRef names a deployment policy that may allow automatic execution
	// of a low-risk reversible action.
	PolicyRef string
}

// Result explains the decision.
type Result struct {
	Decision         Decision
	Risk             domain.RiskLevel
	Reason           string
	RequiresApproval bool
	MatchedRules     []string
	PolicyVersion    string
}

// Allowed reports whether the action may run without approval.
func (r Result) Allowed() bool { return r.Decision == Allow }

// Config is the deployment policy.
type Config struct {
	// Version identifies the rule set that produced a decision. It is stored
	// with the action contract so a later review can tell which rules applied.
	Version string
	// AllowedRoots bounds every declared path.
	AllowedRoots []string
	// AutoApproveRefs names policy refs whose low-risk reversible actions may
	// run without approval.
	AutoApproveRefs []string
}

// Engine evaluates requests against one configuration.
type Engine struct {
	version  string
	roots    []string
	autoRefs map[string]struct{}
}

// New validates cfg and builds an engine.
func New(cfg Config) (*Engine, error) {
	if strings.TrimSpace(cfg.Version) == "" {
		return nil, fmt.Errorf("policy version must not be empty")
	}
	if len(cfg.AllowedRoots) == 0 {
		return nil, fmt.Errorf("policy requires at least one allowed root")
	}

	roots := make([]string, 0, len(cfg.AllowedRoots))
	for _, root := range cfg.AllowedRoots {
		normalized, err := normalizePath(root)
		if err != nil {
			return nil, fmt.Errorf("allowed root %q: %w", root, err)
		}
		roots = append(roots, normalized)
	}
	sort.Strings(roots)

	autoRefs := make(map[string]struct{}, len(cfg.AutoApproveRefs))
	for _, ref := range cfg.AutoApproveRefs {
		if trimmed := strings.TrimSpace(ref); trimmed != "" {
			autoRefs[trimmed] = struct{}{}
		}
	}

	return &Engine{
		version:  cfg.Version,
		roots:    roots,
		autoRefs: autoRefs,
	}, nil
}

// Evaluate applies every rule and combines the findings.
//
// Rules do not short-circuit: the answer must reflect the worst thing the
// action could do, not the first thing that happened to match.
func (e *Engine) Evaluate(req Request) Result {
	result := Result{
		Decision:      Allow,
		Risk:          domain.RiskR0,
		PolicyVersion: e.version,
	}

	note := func(risk domain.RiskLevel, rule, reason string) {
		result.MatchedRules = append(result.MatchedRules, rule)
		if risk.Rank() > result.Risk.Rank() {
			result.Risk = risk
			result.Reason = reason
		} else if result.Reason == "" {
			result.Reason = reason
		}
	}

	deny := func(risk domain.RiskLevel, rule, reason string) {
		note(risk, rule, reason)
		result.Decision = Deny
	}

	// 1. The action must be classifiable. An unknown kind cannot be reasoned
	// about, so it fails closed.
	baseRisk, ok := kindRisk[req.Kind]
	if !ok {
		deny(domain.RiskR4, "unknown_kind", fmt.Sprintf("unknown action kind %q", req.Kind))
	}
	if strings.TrimSpace(req.Command) == "" {
		deny(domain.RiskR4, "empty_command", "action has no command to evaluate")
	}

	// 2. Destructive patterns are refused before anything else matters.
	for _, pattern := range dangerousPatterns {
		if pattern.expression.MatchString(req.Command) {
			deny(domain.RiskR4, pattern.id, pattern.reason)
		}
	}

	// 3. Privilege escalation is never granted through this path.
	if escalatingPattern.MatchString(req.Command) {
		deny(domain.RiskR4, "privilege_escalation", "command attempts to gain privileges (sudo/su/doas/pkexec)")
	}
	switch strings.ToLower(strings.TrimSpace(req.RunAs)) {
	case "", "root", "0":
		if strings.TrimSpace(req.RunAs) == "" {
			deny(domain.RiskR3, "missing_identity", "action does not declare an execution identity")
		} else {
			deny(domain.RiskR4, "root_identity", "actions must not run as root")
		}
	}

	// 4. Every declared path must sit inside an allowed root.
	for _, raw := range req.Paths {
		normalized, err := normalizePath(raw)
		if err != nil {
			deny(domain.RiskR3, "unusable_path", fmt.Sprintf("path %q cannot be evaluated: %v", raw, err))
			continue
		}
		if !e.within(normalized) {
			deny(domain.RiskR3, "path_outside_roots", fmt.Sprintf("path %s is outside the allowed roots", normalized))
		}
	}

	// 5. A read-only action must not declare a writable target.
	if req.Kind == KindObserve && len(req.Paths) > 0 {
		deny(domain.RiskR3, "observe_with_target", "a read-only action declared target resources")
	}

	// 6. The effect decides the floor.
	if ok {
		note(baseRisk, "kind_baseline", fmt.Sprintf("action kind %s is classified %s", req.Kind, baseRisk))
	}

	// 7. An irreversible change cannot be treated as low risk.
	if req.Kind == KindModifyFile && !req.Reversible {
		note(domain.RiskR2, "irreversible_change", "file change has no defined compensating action")
	}

	if result.Decision == Deny {
		result.RequiresApproval = false
		return result
	}

	switch result.Risk {
	case domain.RiskR0:
		result.Decision = Allow
	case domain.RiskR1:
		if req.Reversible && e.autoApproved(req.PolicyRef) {
			result.Decision = Allow
			result.MatchedRules = append(result.MatchedRules, "auto_approved_policy_ref")
			if result.Reason == "" {
				result.Reason = "reversible low-risk action allowed by deployment policy"
			}
		} else {
			result.Decision = RequiresApproval
		}
	case domain.RiskR2:
		result.Decision = RequiresApproval
	default:
		// R3 and R4 are refused; R3 needs a restricted identity and a second
		// confirmation that this path does not provide.
		result.Decision = Deny
		if result.Reason == "" {
			result.Reason = fmt.Sprintf("risk level %s is refused", result.Risk)
		}
	}

	result.RequiresApproval = result.Decision == RequiresApproval
	return result
}

func (e *Engine) autoApproved(ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	_, ok := e.autoRefs[ref]
	return ok
}

// within reports whether an absolute POSIX path is inside an allowed root.
func (e *Engine) within(candidate string) bool {
	for _, root := range e.roots {
		if root == "/" {
			return true
		}
		if candidate == root || strings.HasPrefix(candidate, root+"/") {
			return true
		}
	}
	return false
}

// normalizePath cleans an absolute POSIX path.
//
// path.Clean resolves "." and ".." lexically, so "/allowed/../../etc/passwd"
// becomes "/etc/passwd" and is rejected by the root check. Symlinks are not
// resolved here: they are handled by the executor when it opens the real file.
func normalizePath(raw string) (string, error) {
	if strings.ContainsRune(raw, '\x00') {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	if !strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("path is not absolute")
	}
	cleaned := path.Clean(raw)
	if cleaned == "." {
		return "", fmt.Errorf("path is empty")
	}
	return cleaned, nil
}

var kindRisk = map[Kind]domain.RiskLevel{
	KindObserve:         domain.RiskR0,
	KindModifyFile:      domain.RiskR1,
	KindServiceControl:  domain.RiskR2,
	KindPackageManage:   domain.RiskR3,
	KindPrivilegeChange: domain.RiskR4,
}

type dangerousPattern struct {
	id         string
	expression *regexp.Regexp
	reason     string
}

// dangerousPatterns are cheap, high-signal refusals. They are not an AST and
// must not be treated as one: the runtime boundary, not this list, is what
// actually contains a script.
var dangerousPatterns = []dangerousPattern{
	{"delete_root", regexp.MustCompile(`\brm\s+(-[A-Za-z]*[rR][A-Za-z]*[fF]?[A-Za-z]*\s+|-[A-Za-z]*[fF][A-Za-z]*\s+)*(/|/\*)(\s|$|[;&|])`),
		"command deletes the filesystem root"},
	{"format_device", regexp.MustCompile(`\bmkfs(\.[a-z0-9]+)?\b`),
		"command formats a filesystem"},
	{"write_block_device", regexp.MustCompile(`\bdd\b[^\n]*\bof=/dev/`),
		"command writes directly to a block device"},
	{"fork_bomb", regexp.MustCompile(`:\s*\(\s*\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`),
		"command is a fork bomb"},
	{"world_writable_root", regexp.MustCompile(`\bchmod\s+(-[A-Za-z]+\s+)*777\s+/(\s|$)`),
		"command makes the filesystem root world-writable"},
	{"remote_code_execution", regexp.MustCompile(`\b(curl|wget)\b[^\n|]*\|\s*(sudo\s+)?(bash|sh|zsh)\b`),
		"command pipes remote content straight into a shell"},
	{"shutdown_host", regexp.MustCompile(`\b(shutdown|poweroff|halt|reboot)\b`),
		"command changes host availability"},
}

var escalatingPattern = regexp.MustCompile(`\b(sudo|su|doas|pkexec)\b`)
