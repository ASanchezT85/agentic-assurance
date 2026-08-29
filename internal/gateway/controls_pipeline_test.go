package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/control"
	"agentic-assurance/internal/fleet"
)

// The control stage on the hot path.
//
// The store is exercised in integration; what is checked here is the pipeline's own
// two decisions: a control in force stops an order the grant and the policy both
// allow, and a control store that cannot be read denies rather than waving everything
// through.

type memControls struct {
	controls []control.Control
	err      error

	// used counts what each throttle has spent, which is the store's job in
	// production and is modelled here so the pipeline's own decision is tested rather
	// than PostgreSQL's.
	used       map[string]int
	consumeErr error
}

func (m *memControls) InForce(_ context.Context, _ string, _ time.Time) ([]control.Control, error) {
	return m.controls, m.err
}

func readOnlyFor(agentID string, at time.Time) control.Control {
	return control.Control{
		ControlID: "ctl_test",
		Action:    fleet.ControlReadOnly,
		CohortID:  "cohort_test",
		AgentID:   agentID,
		Reason:    "correlated liquidation",
		AppliedAt: at.Add(-time.Minute),
		ExpiresAt: at.Add(time.Hour),
	}
}

func TestAControlInForceStopsAnOtherwiseValidOrder(t *testing.T) {
	p, fake, _ := harness(t)
	p.Controls = &memControls{controls: []control.Control{readOnlyFor("agent_test", at)}}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if result.Accepted {
		t.Fatal("a READ_ONLY control did not stop the order")
	}
	if result.Stage != StageControl || result.Code != "CONTROL_READ_ONLY" {
		t.Errorf("stage/code = %s/%s, want CONTROL/CONTROL_READ_ONLY: %s",
			result.Stage, result.Code, result.Reason)
	}
	if fake.Submissions("coid-idem-01J8Z3K9QW") != 0 {
		t.Error("the order reached the venue anyway")
	}
}

// Fail closed, for the reason an unreadable grant store denies: a control is in force
// until someone revokes it, and reading "cannot reach the database" as "no control
// applies" would unenforce every one of them exactly when the database is unhealthy.
func TestAnUnreadableControlStoreDenies(t *testing.T) {
	p, fake, _ := harness(t)
	p.Controls = &memControls{err: errors.New("connection refused")}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if result.Accepted {
		t.Fatal("an unreadable control store allowed the order")
	}
	if result.Stage != StageControl || result.Code != "CONTROL_UNAVAILABLE" {
		t.Errorf("stage/code = %s/%s, want CONTROL/CONTROL_UNAVAILABLE", result.Stage, result.Code)
	}
	if fake.Submissions("coid-idem-01J8Z3K9QW") != 0 {
		t.Error("the order reached the venue anyway")
	}
}

// A control on somebody else must not stop this agent. A scope that bit everyone would
// turn one cohort's incident into a tenant-wide outage.
func TestAControlOnAnotherAgentDoesNotStopThisOne(t *testing.T) {
	p, _, _ := harness(t)
	p.Controls = &memControls{controls: []control.Control{readOnlyFor("agent_someone_else", at)}}

	if result := p.Submit(context.Background(), envelope(nil), presentedAPI()); !result.Accepted {
		t.Fatalf("a control scoped to another agent refused this one: %s %s",
			result.Stage, result.Code)
	}
}

func (m *memControls) Consume(_ context.Context, _ string, c control.Control,
	key string, _ time.Time) (bool, int, error) {

	if m.consumeErr != nil {
		return false, 0, m.consumeErr
	}
	if m.used == nil {
		m.used = map[string]int{}
	}
	if m.used[c.ControlID] >= c.MaxOrders {
		return false, m.used[c.ControlID], nil
	}
	m.used[c.ControlID]++
	return true, m.used[c.ControlID], nil
}

func throttleFor(agentID string, at time.Time, max int) control.Control {
	c := readOnlyFor(agentID, at)
	c.ControlID = "ctl_throttle"
	c.Action = fleet.ControlThrottle
	c.MaxOrders = max
	c.Window = time.Minute
	return c
}

// A throttle allows up to its rate and then refuses, naming what it counted.
func TestAThrottleRefusesOnceItsWindowIsSpent(t *testing.T) {
	p, _, _ := harness(t)
	p.Controls = &memControls{controls: []control.Control{throttleFor("agent_test", at, 1)}}

	if result := p.Submit(context.Background(), envelope(nil), presentedAPI()); !result.Accepted {
		t.Fatalf("the first order under a throttle of 1 was refused: %s %s",
			result.Stage, result.Code)
	}

	second := envelope(func(m map[string]any) {
		m["idempotency_key"] = "idem-second"
		m["envelope_id"] = "env_second"
	})
	result := p.Submit(context.Background(), second, presentedAPI())
	if result.Accepted {
		t.Fatal("a second order was accepted under a throttle of one per minute")
	}
	if result.Stage != StageControl || result.Code != "CONTROL_THROTTLED" {
		t.Errorf("stage/code = %s/%s, want CONTROL/CONTROL_THROTTLED", result.Stage, result.Code)
	}
	if !strings.Contains(result.Reason, "1 orders per 1m0s") {
		t.Errorf("the refusal does not say what the rate is: %s", result.Reason)
	}
}

// A stronger control decides first. An operator who authorized a READ_ONLY and a
// throttle over the same scope must be told the scope is stopped, not that it is
// going too fast.
func TestAStopBeatsAThrottleOnTheSameScope(t *testing.T) {
	p, _, _ := harness(t)
	p.Controls = &memControls{controls: []control.Control{
		throttleFor("agent_test", at, 10),
		readOnlyFor("agent_test", at),
	}}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if result.Code != "CONTROL_READ_ONLY" {
		t.Errorf("code = %s, want CONTROL_READ_ONLY", result.Code)
	}
}

// A counter that cannot be read denies, for the reason an unreadable store does: a
// throttle that fails open is not a throttle.
func TestAnUncountableThrottleDenies(t *testing.T) {
	p, _, _ := harness(t)
	p.Controls = &memControls{
		controls:   []control.Control{throttleFor("agent_test", at, 5)},
		consumeErr: errors.New("connection refused"),
	}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if result.Accepted || result.Code != "CONTROL_UNAVAILABLE" {
		t.Errorf("accepted=%v code=%s, want CONTROL_UNAVAILABLE", result.Accepted, result.Code)
	}
}
