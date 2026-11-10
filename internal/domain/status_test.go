package domain_test

import (
	"errors"
	"testing"

	"github.com/gdemonc/grad-ops/internal/domain"
)

func TestCanTransitionAllowsTheDocumentedPath(t *testing.T) {
	path := []domain.Status{
		domain.StatusQueued,
		domain.StatusInvestigating,
		domain.StatusAwaitingApproval,
		domain.StatusExecuting,
		domain.StatusVerifying,
		domain.StatusSealed,
	}
	for i := 0; i+1 < len(path); i++ {
		if !domain.CanTransition(path[i], path[i+1]) {
			t.Fatalf("expected %s -> %s to be allowed", path[i], path[i+1])
		}
	}

	rollback := []domain.Status{
		domain.StatusVerifying,
		domain.StatusRollingBack,
		domain.StatusRolledBack,
	}
	for i := 0; i+1 < len(rollback); i++ {
		if !domain.CanTransition(rollback[i], rollback[i+1]) {
			t.Fatalf("expected %s -> %s to be allowed", rollback[i], rollback[i+1])
		}
	}
}

func TestCanTransitionRejectsSkippedStages(t *testing.T) {
	cases := []struct {
		from domain.Status
		to   domain.Status
	}{
		{domain.StatusQueued, domain.StatusSealed},
		{domain.StatusQueued, domain.StatusExecuting},
		{domain.StatusInvestigating, domain.StatusExecuting},
		{domain.StatusAwaitingApproval, domain.StatusVerifying},
		{domain.StatusSealed, domain.StatusInvestigating},
		{domain.StatusFailed, domain.StatusQueued},
		{domain.StatusRejected, domain.StatusInvestigating},
		{domain.StatusRollingBack, domain.StatusSealed},
		{domain.StatusRolledBack, domain.StatusSealed},
		{domain.StatusRecoveryFailed, domain.StatusRolledBack},
	}

	for _, tc := range cases {
		if domain.CanTransition(tc.from, tc.to) {
			t.Errorf("expected %s -> %s to be rejected", tc.from, tc.to)
		}
	}
}

func TestCanTransitionRejectsUnknownStatuses(t *testing.T) {
	if domain.CanTransition("BOGUS", domain.StatusInvestigating) {
		t.Fatal("expected unknown source status to be rejected")
	}
	if domain.CanTransition(domain.StatusQueued, "BOGUS") {
		t.Fatal("expected unknown target status to be rejected")
	}
}

func TestTerminalStatuses(t *testing.T) {
	terminal := []domain.Status{
		domain.StatusSealed,
		domain.StatusRolledBack,
		domain.StatusRecoveryFailed,
		domain.StatusFailed,
		domain.StatusRejected,
	}
	for _, status := range terminal {
		if !status.Terminal() {
			t.Errorf("expected %s to be terminal", status)
		}
	}

	active := []domain.Status{
		domain.StatusQueued,
		domain.StatusInvestigating,
		domain.StatusAwaitingApproval,
		domain.StatusExecuting,
		domain.StatusVerifying,
		domain.StatusRollingBack,
	}
	for _, status := range active {
		if status.Terminal() {
			t.Errorf("expected %s not to be terminal", status)
		}
	}
}

func TestValidateTransitionReturnsTypedError(t *testing.T) {
	err := domain.ValidateTransition(domain.StatusQueued, domain.StatusSealed)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("expected ErrInvalidTransition, got %v", err)
	}

	var transitionErr *domain.TransitionError
	if !errors.As(err, &transitionErr) {
		t.Fatalf("expected *domain.TransitionError, got %T", err)
	}
	if transitionErr.From != domain.StatusQueued || transitionErr.To != domain.StatusSealed {
		t.Fatalf("unexpected transition in error: %+v", transitionErr)
	}
}

func TestParseStatus(t *testing.T) {
	if _, err := domain.ParseStatus("SEALED"); err != nil {
		t.Fatalf("expected SEALED to parse: %v", err)
	}
	if _, err := domain.ParseStatus("sealed"); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("expected lowercase status to be rejected, got %v", err)
	}
	if _, err := domain.ParseStatus(""); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("expected empty status to be rejected, got %v", err)
	}
}

func TestRiskLevelOrderingAndValidation(t *testing.T) {
	if domain.RiskR0.Rank() >= domain.RiskR4.Rank() {
		t.Fatal("expected R0 to rank below R4")
	}
	if !domain.RiskR2.Valid() {
		t.Fatal("expected R2 to be valid")
	}
	if domain.RiskLevel("R9").Valid() {
		t.Fatal("expected R9 to be invalid")
	}
	if domain.RiskLevel("R9").Rank() <= domain.RiskR4.Rank() {
		t.Fatal("expected an unknown level to rank worse than R4")
	}
}
