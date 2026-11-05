package domain

import "fmt"

// RiskLevel is the deterministic risk classification of a proposal.
//
// The level is assigned by the policy engine from the subject identity, the
// target resource, the side effect, the blast radius and the deployment
// policy. It is never taken from model output.
type RiskLevel string

const (
	// RiskR0 is a read-only observation that does not change system state.
	RiskR0 RiskLevel = "R0"
	// RiskR1 is a low-risk, reversible, bounded change. It still requires
	// approval unless an explicit deployment policy binds the exact tool and
	// arguments.
	RiskR1 RiskLevel = "R1"
	// RiskR2 affects a single service or file and requires human approval.
	RiskR2 RiskLevel = "R2"
	// RiskR3 has high impact and is denied by default or requires a
	// restricted identity plus a second confirmation.
	RiskR3 RiskLevel = "R3"
	// RiskR4 is forbidden and cannot be released by ordinary approval.
	RiskR4 RiskLevel = "R4"
)

var riskOrder = map[RiskLevel]int{
	RiskR0: 0,
	RiskR1: 1,
	RiskR2: 2,
	RiskR3: 3,
	RiskR4: 4,
}

// Valid reports whether r is a defined risk level.
func (r RiskLevel) Valid() bool {
	_, ok := riskOrder[r]
	return ok
}

// Rank returns the numeric severity of r, where R0 is the lowest. An invalid
// level ranks highest so that ordering comparisons fail closed.
func (r RiskLevel) Rank() int {
	if rank, ok := riskOrder[r]; ok {
		return rank
	}
	return 5
}

// ParseRiskLevel validates a serialized risk level.
func ParseRiskLevel(raw string) (RiskLevel, error) {
	level := RiskLevel(raw)
	if !level.Valid() {
		return "", fmt.Errorf("%w: unknown risk level %q", ErrInvalidRequest, raw)
	}
	return level, nil
}
