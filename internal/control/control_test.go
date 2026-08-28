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

// THROTTLE has no counter here, so it must not be storable. A control the platform
// records and does not apply is exactly the shadow-mode confusion this package ends:
// an operator would read the list, see the throttle, and believe it was throttling.
func TestThrottleIsNotEnforceableInThisBuild(t *testing.T) {
	if Enforceable(fleet.ControlThrottle) {
		t.Fatal("THROTTLE claims an enforcement path")
	}
	at := time.Now().UTC()
	c := readOnly(at)
	c.Action = fleet.ControlThrottle
	if d := Evaluate([]Control{c}, envelope(), at); !d.Allowed {
		t.Error("THROTTLE refused an order through a path nothing implements")
	}
}
