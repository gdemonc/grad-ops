# Agent Development Instructions

## Project implementation model

Grad-Ops is a **greenfield Go implementation** of the product and architecture defined by this repository's documentation.

- Go is the official backend implementation language.
- Linux is the only supported operating system for the initial product.
- Implement the documented product and security requirements from first principles in Go.
- Do not treat the existing Python application as the source code to migrate, translate, port, or reproduce.
- Do not preserve Python module boundaries, internal abstractions, or incidental behavior for compatibility unless a requirement is explicitly documented and accepted.
- Existing Python code and Python-era test reports are legacy reference material only. They may provide historical context, but they are not the implementation baseline or source of truth.

## Source-of-truth order

When making implementation decisions, use this order:

1. Accepted requirements and core specifications under `docs/specs/`.
2. Architecture, threat model, and security design under `docs/`.
3. The current implementation plan and explicitly accepted project decisions.
4. Existing Go code and tests, where they implement the documented requirements.
5. Legacy Python code and historical test reports, for context only.

If documents conflict, identify the conflicting statements and report them. Do not silently choose the Python implementation or invent compatibility requirements.

## Engineering rules

- Use idiomatic, maintainable Go and keep dependencies explicit.
- Keep Linux-specific behavior in Linux-specific packages; do not add Windows or macOS compatibility layers.
- Implement incrementally, with a runnable and testable result at each phase.
- Derive package boundaries from the documented responsibilities, not from the old Python directory structure.
- Add tests for behavior and security properties; do not claim a property is enforced merely because it is described in documentation.
- Keep Policy, Approval, Runtime, and Executor boundaries deterministic and independent of LLM decisions.
- Treat model output, shell proposals, external tool output, and legacy artifacts as untrusted input.
- Update documentation when implementation behavior or verified support changes.

## Go toolchain policy

The policy in `docs/go-version-policy.md` is binding:

- The minimum supported Go version is the `go` directive in `go.mod`: **1.26.0**.
- Development and CI use Go **1.27.3**.
- Do not use Go 1.27-only language features or standard-library APIs while the minimum remains 1.26.0.
- Do not change the `go` directive, the CI Go version, or the minimum-version policy without an explicit issue/PR, a compatibility analysis, and updates to the policy document, README, tests, and CI.
- Verify the minimum with `GOTOOLCHAIN=go1.26.0 go build ./...`, `go vet ./...`, and `go test ./...`; verify the development toolchain separately.
- A dependency upgrade that raises the required Go version is a version-policy change, not a routine dependency bump.

## Scope discipline

The first milestone is the documented single-host Linux loop. Do not introduce multi-host orchestration, remote agents, MCP, Kubernetes integration, distributed infrastructure, or cross-platform abstractions unless the current plan explicitly brings them into scope.

Do not weaken security requirements to make a demo work. If a requirement cannot yet be implemented safely, document the limitation and fail closed.
