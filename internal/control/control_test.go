package control

import (
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/intent"
)

func envelope() *intent.AgentExecutionEnvelope {
	e := &intent.AgentExecutionEnvelope{}
	e.Agent.AgentID = "agent_1"
	e.Principal.AccountID = "acct_1"
	return e
}

func readOnly(at time.Time) Control {
	return Control{
		ControlID: "ctl_1",
		Action:    fleet.ControlReadOnly,
		CohortID:  "cohort_1",
		AppliedAt: at,
		ExpiresAt: at.Add(time.Hour),
		Reason:    "correlated liquidation",
	}
}

func TestATenantWideReadOnlyControlRefuses(t *testing.T) {
	at := time.Now().UTC()
	d := Evaluate([]Control{readOnly(at)}, envelope(), at)
	if d.Allowed || d.Code != "CONTROL_READ_ONLY" {
		t.Fatalf("allowed=%v code=%q", d.Allowed, d.Code)
	}
	if d.ControlID != "ctl_1" {
		t.Error("the denial does not name the control that produced it")
	}
}

// A control that expired is not a control. Enforcing one would throttle an agent
// forever because of an incident last spring.
func TestAnExpiredControlDoesNotBind(t *testing.T) {
	at := time.Now().UTC()
	c := readOnly(at.Add(-2 * time.Hour))
	c.ExpiresAt = at.Add(-time.Hour)

	if d := Evaluate([]Control{c}, envelope(), at); !d.Allowed {
		t.Fatalf("an expired control refused an order: %s", d.Code)
	}
}

func TestARevokedControlDoesNotBind(t *testing.T) {
	at := time.Now().UTC()
	c := readOnly(at)
	revoked := at
	c.RevokedAt = &revoked

	if d := Evaluate([]Control{c}, envelope(), at); !d.Allowed {
		t.Fatalf("a revoked control refused an order: %s", d.Code)
	}
}

// Scope is the part worth guarding. A control on one agent that stopped the whole
// tenant would be an outage, and one that stopped nobody would be theatre.
func TestAScopedControlBindsOnlyItsScope(t *testing.T) {
	at := time.Now().UTC()

	byAgent := readOnly(at)
	byAgent.AgentID = "agent_other"
	if d := Evaluate([]Control{byAgent}, envelope(), at); !d.Allowed {
		t.Error("a control scoped to another agent refused this one")
	}

	byAgent.AgentID = "agent_1"
	if d := Evaluate([]Control{byAgent}, envelope(), at); d.Allowed {
		t.Error("a control scoped to this agent did not bind")
	}

	byAccount := readOnly(at)
	byAccount.AccountID = "acct_other"
	if d := Evaluate([]Control{byAccount}, envelope(), at); !d.Allowed {
		t.Error("a control scoped to another account refused this one")
	}
}

func TestEachActionCarriesItsOwnCode(t *testing.T) {
	at := time.Now().UTC()
	for action, want := range map[fleet.ControlAction]string{
		fleet.ControlReadOnly:        "CONTROL_READ_ONLY",
		fleet.ControlIsolateCohort:   "CONTROL_COHORT_ISOLATED",
		fleet.ControlRequireApproval: "CONTROL_APPROVAL_REQUIRED",
	} {
		c := readOnly(at)
		c.Action = action
		if d := Evaluate([]Control{c}, envelope(), at); d.Code != want {
			t.Errorf("%s produced %q, want %q", action, d.Code, want)
		}
	}
}

// Evaluate never decides a throttle: a rate cannot be judged from one request. It
// passes, and the caller consumes a slot against each control Throttles returns.
func TestEvaluateLeavesThrottlesToTheLimiter(t *testing.T) {
	at := time.Now().UTC()
	c := readOnly(at)
	c.Action = fleet.ControlThrottle
	c.MaxOrders, c.Window = 1, time.Minute

	if d := Evaluate([]Control{c}, envelope(), at); !d.Allowed {
		t.Errorf("Evaluate refused a throttle on its own: %s", d.Code)
	}
	if got := Throttles([]Control{c}, envelope(), at); len(got) != 1 {
		t.Errorf("Throttles returned %d controls, want 1", len(got))
	}
}

// The same scoping and lifetime rules as everything else. A throttle nobody scoped to
// this agent, or one that expired, must not reach the counter at all: consuming a slot
// against it would rate-limit an agent no control applies to.
func TestThrottlesRespectScopeAndLifetime(t *testing.T) {
	at := time.Now().UTC()

	elsewhere := readOnly(at)
	elsewhere.Action = fleet.ControlThrottle
	elsewhere.AgentID = "agent_other"
	if got := Throttles([]Control{elsewhere}, envelope(), at); len(got) != 0 {
		t.Error("a throttle scoped to another agent was returned for this one")
	}

	expired := readOnly(at.Add(-2 * time.Hour))
	expired.Action = fleet.ControlThrottle
	expired.ExpiresAt = at.Add(-time.Hour)
	if got := Throttles([]Control{expired}, envelope(), at); len(got) != 0 {
		t.Error("an expired throttle was returned")
	}
}

// All four actions enforce now. THROTTLE was refused at authorization for as long as
// nothing counted orders.
func TestEveryControlActionIsEnforceable(t *testing.T) {
	for _, a := range []fleet.ControlAction{
		fleet.ControlThrottle, fleet.ControlReadOnly,
		fleet.ControlIsolateCohort, fleet.ControlRequireApproval,
	} {
		if !Enforceable(a) {
			t.Errorf("%s has no enforcement path", a)
		}
	}
	if Enforceable(fleet.ControlAction("SOMETHING_ELSE")) {
		t.Error("an unknown action claims an enforcement path")
	}
}

// A cohort's members, named. ISOLATE_COHORT was an action whose name was a lie: a
// cohort is a predicate and the only scopes were one agent or the whole tenant, so
// answering a cohort incident meant stopping everyone or authorizing one control each.
func TestAControlCanNameSeveralAgents(t *testing.T) {
	at := time.Now().UTC()
	c := readOnly(at)
	c.AgentIDs = []string{"agent_2", "agent_1", "agent_3"}

	if d := Evaluate([]Control{c}, envelope(), at); d.Allowed {
		t.Error("an agent named in the list was not covered")
	}

	c.AgentIDs = []string{"agent_2", "agent_3"}
	if d := Evaluate([]Control{c}, envelope(), at); !d.Allowed {
		t.Errorf("an agent not in the list was refused: %s", d.Code)
	}
}
