package domain

import "fmt"

// Status is the lifecycle state of a task.
//
// The state machine is the only sanctioned way to change a task's status. It
// exists so that a task can never skip investigation, authorization, execution
// or independent verification.
type Status string

const (
	// StatusQueued is a task that has been accepted but not yet investigated.
	StatusQueued Status = "QUEUED"
	// StatusInvestigating is a task whose read-only investigation is running.
	StatusInvestigating Status = "INVESTIGATING"
	// StatusSealed is a task whose outcome is final: either the read-only
	// investigation completed, or a side effect was verified independently.
	StatusSealed Status = "SEALED"
	// StatusAwaitingApproval is a task with a side effect waiting for a human.
	StatusAwaitingApproval Status = "AWAITING_APPROVAL"
	// StatusExecuting is a task whose approved side effect is running.
	StatusExecuting Status = "EXECUTING"
	// StatusVerifying is a task whose side effect has run and is being
	// verified through a fresh observation.
	StatusVerifying Status = "VERIFYING"
	// StatusRollingBack is a task applying its compensating action.
	StatusRollingBack Status = "ROLLING_BACK"
	// StatusRolledBack is a task whose rollback ran and was verified.
	StatusRolledBack Status = "ROLLED_BACK"
	// StatusRecoveryFailed is a task whose rollback failed or could not be
	// proven.
	StatusRecoveryFailed Status = "RECOVERY_FAILED"
	// StatusFailed is a task that ended in a system error, an exhausted
	// budget, or an unclassified failure.
	StatusFailed Status = "FAILED"
	// StatusRejected is a task whose proposed action was refused by policy or
	// by a human reviewer. No rejected side effect happened.
	StatusRejected Status = "REJECTED"
)

// transitions lists the states reachable from each state.
//
// ROLLING_BACK deliberately does not allow FAILED: a failed rollback is
// RECOVERY_FAILED, and conflating the two would let a recovery failure look
// like an ordinary task failure.
var transitions = map[Status][]Status{
	StatusQueued:           {StatusInvestigating, StatusFailed, StatusRejected},
	StatusInvestigating:    {StatusSealed, StatusAwaitingApproval, StatusFailed, StatusRejected},
	StatusAwaitingApproval: {StatusExecuting, StatusFailed, StatusRejected},
	StatusExecuting:        {StatusVerifying, StatusFailed, StatusRejected},
	StatusVerifying:        {StatusSealed, StatusRollingBack, StatusFailed},
	StatusRollingBack:      {StatusRolledBack, StatusRecoveryFailed},
}

var terminal = map[Status]bool{
	StatusSealed:         true,
	StatusRolledBack:     true,
	StatusRecoveryFailed: true,
	StatusFailed:         true,
	StatusRejected:       true,
}

// Valid reports whether s is a defined status.
func (s Status) Valid() bool {
	if _, ok := transitions[s]; ok {
		return true
	}
	return terminal[s]
}

// Terminal reports whether s is a final state.
func (s Status) Terminal() bool {
	return terminal[s]
}

// CanTransition reports whether moving from one status to another is allowed.
func CanTransition(from, to Status) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	for _, candidate := range transitions[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// ValidateTransition returns an error unless from may move to to.
func ValidateTransition(from, to Status) error {
	if CanTransition(from, to) {
		return nil
	}
	return &TransitionError{From: from, To: to}
}

// TransitionError reports a rejected state change.
type TransitionError struct {
	From Status
	To   Status
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("%v: %s -> %s", ErrInvalidTransition, e.From, e.To)
}

// Unwrap lets callers test a TransitionError with errors.Is.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// ParseStatus validates a serialized status.
func ParseStatus(raw string) (Status, error) {
	status := Status(raw)
	if !status.Valid() {
		return "", fmt.Errorf("%w: unknown status %q", ErrInvalidRequest, raw)
	}
	return status, nil
}
