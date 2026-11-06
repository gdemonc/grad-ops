package investigation

import (
	"context"
	"fmt"
)

// StubPlanner is a deterministic stand-in for a model.
//
// It runs a fixed read-only probe set and concludes only that the probes ran;
// it never interprets the request. It exists so the queue, the shell, the
// evidence store, the policy and the approval flow can be exercised before a
// model is wired in. Replacing it does not change any of that machinery.
type StubPlanner struct {
	// Probes run in order. They must be read-only.
	Probes []string
	// Action, when set, is proposed after the probes run. Leaving it nil keeps
	// the planner read-only.
	Action *Action
}

// Plan implements Planner.
func (p StubPlanner) Plan(context.Context, string) (Plan, error) {
	if len(p.Probes) == 0 {
		return Plan{}, fmt.Errorf("%w: the stub planner has no probes", ErrInvalidPlan)
	}

	citations := make([]int, 0, len(p.Probes))
	for i := range p.Probes {
		citations = append(citations, i+1)
	}

	return Plan{
		Probes:    p.Probes,
		Action:    p.Action,
		Summary:   fmt.Sprintf("ran %d read-only probes; no conclusion was drawn", len(p.Probes)),
		Citations: citations,
	}, nil
}
