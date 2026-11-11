package investigation

import (
	"strings"
	"testing"
)

func TestBoundedAgentObservationsKeepsTheNewestWithinTheBudget(t *testing.T) {
	service := &Service{cfg: Config{AgentContextBytes: 120}}
	observations := []Observation{
		{Index: 1, Command: "old", Content: strings.Repeat("a", 100)},
		{Index: 2, Command: "new", Content: strings.Repeat("b", 100)},
	}
	got, omitted := service.boundedAgentObservations(observations)
	if len(got) != 1 || got[0].Index != 2 || omitted != 1 {
		t.Fatalf("kept %+v, omitted=%d", got, omitted)
	}
	if !got[0].Truncated {
		t.Fatalf("an oversized newest observation must be marked truncated: %+v", got[0])
	}
	if len(got[0].Content) > 120 {
		t.Fatalf("context budget exceeded: %d", len(got[0].Content))
	}
}

func TestBoundedAgentObservationsReturnsEverythingWhenItFits(t *testing.T) {
	service := &Service{cfg: Config{AgentContextBytes: 4096}}
	observations := []Observation{
		{Index: 1, Command: "a", Content: "one"},
		{Index: 2, Command: "b", Content: "two"},
	}
	got, omitted := service.boundedAgentObservations(observations)
	if len(got) != 2 || omitted != 0 || got[0].Index != 1 || got[1].Index != 2 {
		t.Fatalf("unexpected bounded observations: %+v omitted=%d", got, omitted)
	}
}
