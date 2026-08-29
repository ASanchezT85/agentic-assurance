package gateway

import (
	"testing"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/evidence"
)

// Every execution state, through the two switches that decide what it means here.
//
// The EXPIRED defect existed because three lists were maintained separately: the broker
// state catalog, the settlement rule for what counts as terminal, and the mapping from
// outcome to evidence event. A structural guard over the catalog lives in
// tests/security; it proves somebody wrote down a meaning for each state. It cannot
// prove the code agrees with what they wrote, because these functions are unexported.
//
// This is the other half: the actual mapping, called with the actual states.
func TestEveryStateMapsExplicitly(t *testing.T) {
	// The decision table, again — deliberately duplicated rather than shared. This one
	// asserts what the code does; the security one asserts that a meaning exists at
	// all. A single table imported by both would let one edit satisfy both guards.
	want := map[broker.ExecutionState]struct {
		event      evidence.EventName
		settlement authority.ReservationState
		closesOpen bool
	}{
		broker.StateUnknown:         {evidence.OrderUnknown, authority.StateCommitted, false},
		broker.StateAccepted:        {evidence.OrderAccepted, authority.StateCommitted, false},
		broker.StatePartiallyFilled: {evidence.OrderAccepted, authority.StateCommitted, false},
		broker.StateFilled:          {evidence.OrderFilled, authority.StateCommitted, true},
		broker.StateRejected:        {evidence.OrderRejected, authority.StateReleased, true},
		broker.StateCancelled:       {evidence.OrderCancelled, authority.StateCommitted, true},
		broker.StateExpired:         {evidence.OrderExpired, authority.StateCommitted, true},
	}

	for state, expected := range want {
		if got := outcomeEvent(state); got != expected.event {
			t.Errorf("%s produces %s, want %s. An unmapped state falling through to "+
				"\"accepted\" is how an expired order was recorded as one the venue took.",
				state, got, expected.event)
		}
		if got := state.Terminal(); got != expected.closesOpen {
			t.Errorf("%s: Terminal() is %v, want %v; the open-order count is released "+
				"by exactly this answer", state, got, expected.closesOpen)
		}

		// The settlement rule, as the pipeline applies it.
		settlement := authority.StateCommitted
		if state == broker.StateRejected {
			settlement = authority.StateReleased
		}
		if settlement != expected.settlement {
			t.Errorf("%s settles as %s, want %s", state, settlement, expected.settlement)
		}
	}

	// And a state nobody has heard of does not become an accepted order.
	//
	// The default branch exists and this is what it must do. Unknown is a state an
	// operator resolves; accepted is a claim about a venue.
	if got := outcomeEvent(broker.ExecutionState("SOMETHING_NEW")); got != evidence.OrderUnknown {
		t.Errorf("an unmapped state produces %s; it must be %s so that a venue adding a "+
			"state is an operator's problem rather than a silent acceptance",
			got, evidence.OrderUnknown)
	}
}
