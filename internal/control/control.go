// Package control is a fleet control a customer authorized, and its enforcement.
//
// internal/fleet makes the distinction a property of the types: a Recommendation has
// no method that enforces, and fleet.Authorize is the only function that produces a
// fleet.Control. Nothing outside its tests ever called it, so every recommendation the
// platform made stopped at shadow mode for want of a surface rather than by decision.
//
// This package stores what a customer authorized and answers one question on the hot
// path: does anything in force refuse this order. It deliberately cannot recommend.
package control

import (
	"strings"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/intent"
)

// Control is an authorized fleet control as it is stored and enforced.
//
// It is not fleet.Control: that type carries the recommendation and the authorization
// as they were at the moment of authorizing, which is the audit record. This is the
// enforcement projection of it — the scope resolved to concrete agents and accounts,
// because the hot path cannot ask the intelligence plane who is in a cohort (INV-005).
type Control struct {
	ControlID  string
	TenantID   string
	IncidentID string
	Action     fleet.ControlAction
	CohortID   string

	// AgentID and AccountID scope the control. Empty means every agent or account in
	// the tenant, which is the ISOLATE_COHORT and READ_ONLY case a customer chose
	// deliberately, never a default that widened by accident: the API requires the
	// scope to be stated.
	AgentID   string
	AccountID string

	AuthorizedBy   string
	PolicyBundleID string
	Reason         string

	AppliedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
	RevokedBy string
}

// InForce reports whether a control still binds at a moment.
func (c Control) InForce(at time.Time) bool {
	return c.RevokedAt == nil && at.Before(c.ExpiresAt)
}

// covers reports whether a control's scope includes an envelope.
func (c Control) covers(env *intent.AgentExecutionEnvelope) bool {
	if c.AgentID != "" && c.AgentID != env.Agent.AgentID {
		return false
	}
	if c.AccountID != "" && c.AccountID != env.Principal.AccountID {
		return false
	}
	return true
}

// Decision is what the control stage concluded.
type Decision struct {
	Allowed bool

	// Code is stable and names the action that refused, so an operator reading a
	// denial knows which control to look at rather than that "a control" refused.
	Code      string
	Reason    string
	ControlID string
}

// Allow is the decision when nothing in force applies.
var Allow = Decision{Allowed: true, Code: "NO_CONTROL_APPLIES"}

// Evaluate applies the controls in force to one envelope.
//
// The first refusal wins and the rest are not consulted: an order refused twice is
// refused, and reporting the first is what makes the code traceable to one control.
//
// THROTTLE is not enforced here and is refused at authorization time instead. A
// control the platform records and does not apply is precisely the shadow-mode
// confusion this package exists to end, and rate limiting an agent needs a counter
// this stage does not have.
func Evaluate(controls []Control, env *intent.AgentExecutionEnvelope, at time.Time) Decision {
	if env == nil {
		return Allow
	}
	for _, c := range controls {
		if !c.InForce(at) || !c.covers(env) {
			continue
		}
		switch c.Action {
		case fleet.ControlReadOnly:
			return refusal(c, "CONTROL_READ_ONLY",
				"a READ_ONLY control is in force for this scope: "+c.Reason)
		case fleet.ControlIsolateCohort:
			return refusal(c, "CONTROL_COHORT_ISOLATED",
				"cohort "+c.CohortID+" is isolated: "+c.Reason)
		case fleet.ControlRequireApproval:
			// Denied rather than parked. V0 has no approval queue, and an order held
			// for an approval nobody can give is an order that silently never
			// happened; the caller is told it needs one.
			return refusal(c, "CONTROL_APPROVAL_REQUIRED",
				"this scope requires human approval and V0 has no approval queue: "+c.Reason)
		}
	}
	return Allow
}

func refusal(c Control, code, reason string) Decision {
	return Decision{Code: code, Reason: reason, ControlID: c.ControlID}
}

// Enforceable reports whether an action has an enforcement path in this build.
func Enforceable(a fleet.ControlAction) bool {
	switch a {
	case fleet.ControlReadOnly, fleet.ControlIsolateCohort, fleet.ControlRequireApproval:
		return true
	}
	return false
}

// ParseAction reads a control action from a request.
func ParseAction(raw string) (fleet.ControlAction, bool) {
	a := fleet.ControlAction(strings.ToUpper(strings.TrimSpace(raw)))
	switch a {
	case fleet.ControlThrottle, fleet.ControlRequireApproval,
		fleet.ControlIsolateCohort, fleet.ControlReadOnly:
		return a, true
	}
	return "", false
}
