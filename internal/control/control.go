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
	"context"
	"fmt"
	"strings"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/intent"
)

// Control is an authorized fleet control as it is stored and enforced.
//
// It is not fleet.Control: that type carries the recommendation and the authorization
// as they were at the moment of authorizing, which is the audit record. This is the
// enforcement projection of it — a scope of concrete agents and accounts, because the
// hot path cannot ask the intelligence plane who is in a cohort (INV-005).
//
// The platform does not expand the cohort predicate into that scope, and four comments
// used to say it did. Membership is measured over a rolling window, and an enforcement
// scope that changed as measurements arrived would be a control nobody authorized. The
// operator names the members; CohortID records which finding it answered.
type Control struct {
	ControlID  string
	TenantID   string
	IncidentID string
	Action     fleet.ControlAction
	CohortID   string

	// AgentID, AgentIDs and AccountID scope the control, and at most one of them is
	// set. All empty means every agent and account in the tenant, which is a scope a
	// customer chooses deliberately and never one reached by leaving fields out: the
	// API requires the scope to be named.
	//
	// AgentIDs is what makes ISOLATE_COHORT usable. A cohort is a predicate, and
	// before this the only scopes were one agent or the whole customer — so answering
	// a cohort incident meant stopping everyone or authorizing one control per agent.
	AgentID   string
	AgentIDs  []string
	AccountID string

	// MaxOrders and Window are the rate a THROTTLE permits, and are zero for every
	// other action. A throttle with no rate is not a lenient throttle, it is an
	// absent one, so the API requires both and the table checks them.
	MaxOrders int
	Window    time.Duration

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
	if len(c.AgentIDs) > 0 && !contains(c.AgentIDs, env.Agent.AgentID) {
		return false
	}
	if c.AccountID != "" && c.AccountID != env.Principal.AccountID {
		return false
	}
	return true
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
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
// THROTTLE is not decided here, because a rate cannot be judged from the request
// alone. Throttling returns the controls that apply, and the caller consumes a slot
// against each: see Throttles and Limiter.
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
		case fleet.ControlThrottle:
			// Decided by the limiter, not here. Left to fall through so a throttle
			// never silently substitutes for the stronger control an operator might
			// have expected: a scope with both a THROTTLE and a READ_ONLY is stopped
			// by the READ_ONLY, whichever was authorized first.
			continue
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

// Throttles returns the THROTTLE controls in force that cover an envelope.
//
// Separate from Evaluate because a rate limit is not a property of the request: it is
// a property of what this scope has already sent, which only the store knows.
func Throttles(controls []Control, env *intent.AgentExecutionEnvelope, at time.Time) []Control {
	if env == nil {
		return nil
	}
	var out []Control
	for _, c := range controls {
		if c.Action == fleet.ControlThrottle && c.InForce(at) && c.covers(env) {
			out = append(out, c)
		}
	}
	return out
}

// Limiter consumes one slot in a throttle's window.
//
// Consuming and checking are one operation on purpose. Two callers that each read the
// count, saw room, and then wrote would both pass, and a rate limit that is only
// approximately a rate limit under load is one that fails exactly when it matters.
type Limiter interface {
	Consume(ctx context.Context, tenantID string, c Control, idempotencyKey string,
		at time.Time) (allowed bool, used int, err error)
}

// Throttled is the refusal a spent window produces.
func Throttled(c Control, used int) Decision {
	return Decision{
		Code: "CONTROL_THROTTLED",
		Reason: fmt.Sprintf("this scope is throttled to %d orders per %s and has sent %d: %s",
			c.MaxOrders, c.Window, used, c.Reason),
		ControlID: c.ControlID,
	}
}

// Enforceable reports whether an action has an enforcement path in this build.
//
// All four now. THROTTLE was refused at authorization for as long as nothing counted
// orders, which left an operator watching a cohort misbehave able to isolate it or
// stop it dead and unable to simply slow it down — the proportionate response, and so
// the one they would reach for first.
func Enforceable(a fleet.ControlAction) bool { return validControl(a) }

func validControl(a fleet.ControlAction) bool {
	switch a {
	case fleet.ControlThrottle, fleet.ControlRequireApproval,
		fleet.ControlIsolateCohort, fleet.ControlReadOnly:
		return true
	}
	return false
}

// ParseAction reads a control action from a request.
func ParseAction(raw string) (fleet.ControlAction, bool) {
	a := fleet.ControlAction(strings.ToUpper(strings.TrimSpace(raw)))
	if validControl(a) {
		return a, true
	}
	return "", false
}
