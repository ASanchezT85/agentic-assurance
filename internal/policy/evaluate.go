package policy

import (
	"agentic-assurance/internal/money"
	"time"

	"agentic-assurance/internal/intent"
)

// Decision is the outcome of hard policy evaluation.
//
// It records the exact bundle that produced it, which is the third Phase 4 exit
// criterion and the thing that makes a historical decision explainable at all: a
// decision without its bundle is an assertion that some policy, once, said no.
type Decision struct {
	Action      Action
	BundleID    string
	Version     int
	ContentHash string
	Status      Status

	// MatchedRules names every rule that fired, in bundle order, with the action
	// each contributed. Reporting only the winner hides that three rules agreed.
	MatchedRules []RuleOutcome

	// DecidedBy is the rule whose action became the decision.
	DecidedBy   string
	Reason      string
	EvaluatedAt time.Time
}

type RuleOutcome struct {
	RuleID string
	Action Action
}

// Evaluate runs compiled policy against an intent.
//
// It is a pure function of its arguments. There is no network call, no clock beyond
// the one passed in, no cache, and no plugin seam: local hard enforcement survives
// the loss of everything outside this process (INV-005), and nothing external can
// contribute to the outcome (INV-003).
//
// Resolution is most-restrictive-wins across every matching rule, not first match.
// First match makes the outcome depend on the order rules happen to appear in, so
// reordering a file would silently change what is enforced.
func Evaluate(b *Bundle, env *intent.AgentExecutionEnvelope, now time.Time) Decision {
	now = now.UTC()

	d := Decision{Action: ActionAllow, EvaluatedAt: now}
	if b == nil {
		// Spec section 17: hard policy unavailable -> DENY.
		return Decision{
			Action:      ActionDeny,
			Reason:      "no policy bundle is loaded; hard policy unavailable denies",
			DecidedBy:   "NO_BUNDLE",
			EvaluatedAt: now,
		}
	}

	d.BundleID = b.BundleID
	d.Version = b.Version
	d.ContentHash = b.ContentHash
	d.Status = b.Activation.Status

	if env == nil {
		d.Action = ActionDeny
		d.DecidedBy = "NO_ENVELOPE"
		d.Reason = "no envelope to evaluate"
		return d
	}

	notional, notionalKnown := effectiveNotional(env.Intent)

	for _, rule := range b.Rules {
		fired, reason := ruleFires(rule, env.Intent, notional, notionalKnown)
		if !fired {
			continue
		}
		d.MatchedRules = append(d.MatchedRules, RuleOutcome{RuleID: rule.ID, Action: rule.Action})

		// First firing rule sets the decision; later ones only tighten it. Ties
		// keep the earlier rule, so the outcome is stable when two rules carry the
		// same action.
		if len(d.MatchedRules) == 1 || MoreRestrictive(rule.Action, d.Action) {
			d.Action = rule.Action
			d.DecidedBy = rule.ID
			d.Reason = reason
		}
	}

	if len(d.MatchedRules) == 0 {
		d.DecidedBy = "NO_RULE_MATCHED"
		d.Reason = "no rule matched; the bundle's default is ALLOW"
	}
	return d
}

// ruleFires reports whether a rule's action applies to this intent.
//
// A rule fires when its `when` clause matches AND its `require` clause fails. A rule
// with no `require` fires on `when` alone, which is how a blanket prohibition like
// OPTIONS_DISABLED works.
func ruleFires(r CompiledRule, in intent.Intent, notional money.Amount, notionalKnown bool) (bool, string) {
	if r.NeedsNotional && !notionalKnown {
		// A size-dependent rule cannot be evaluated against an order of unknown
		// size. Treating that as "does not match" would make every notional rule
		// avoidable by omitting the notional, so it fires instead.
		if r.Action == ActionAllow {
			return false, ""
		}
		return true, "order size cannot be determined without a market price, and this rule depends on it"
	}

	if r.WhenAssetClass != "" && in.AssetClass != r.WhenAssetClass {
		return false, ""
	}
	if r.WhenSide != "" && in.Side != r.WhenSide {
		return false, ""
	}
	if r.WhenOrderType != "" && in.OrderType != r.WhenOrderType {
		return false, ""
	}
	if len(r.WhenInstruments) > 0 && !containsString(r.WhenInstruments, in.InstrumentID) {
		return false, ""
	}
	if r.WhenExtendedHours != nil && in.ExtendedHours != *r.WhenExtendedHours {
		return false, ""
	}
	if r.WhenNotionalGT != nil && !(notional > *r.WhenNotionalGT) {
		return false, ""
	}
	if r.WhenNotionalGTE != nil && !(notional >= *r.WhenNotionalGTE) {
		return false, ""
	}
	if r.WhenNotionalLT != nil && !(notional < *r.WhenNotionalLT) {
		return false, ""
	}
	if r.WhenNotionalLTE != nil && !(notional <= *r.WhenNotionalLTE) {
		return false, ""
	}

	// `when` matched. Now the requirement, if any.
	if r.RequireNotionalLTE != nil && notional > *r.RequireNotionalLTE {
		return true, "notional exceeds the rule's ceiling"
	}
	if r.RequireNotionalGTE != nil && notional < *r.RequireNotionalGTE {
		return true, "notional is below the rule's floor"
	}
	if r.RequireNotionalLTE != nil || r.RequireNotionalGTE != nil {
		return false, "" // requirement holds, so the rule does not fire
	}
	return true, "matched the rule's conditions"
}

// effectiveNotional mirrors the authority package's rule, and for the same reason:
// a market order sized by quantity has no bounded notional until it fills, and there
// is no market data on the hot path (ADR-019). A limit price bounds the exposure; a
// stop price is a trigger, not a fill price.
func effectiveNotional(in intent.Intent) (money.Amount, bool) {
	// Exact, and the same arithmetic authority uses. A policy threshold compared against
	// a float while a grant ceiling is compared against an exact amount would be two
	// different questions asked about one order.
	if in.Notional != nil {
		return *in.Notional, true
	}
	if in.Quantity == nil {
		return 0, false
	}
	switch in.OrderType {
	case intent.OrderLimit, intent.OrderStopLimit:
		if in.LimitPrice != nil {
			notional := money.NotionalOf(*in.LimitPrice, *in.Quantity)
			if notional == 0 && *in.LimitPrice != 0 && *in.Quantity != 0 {
				return 0, false
			}
			return notional, true
		}
	}
	return 0, false
}

func containsString(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
