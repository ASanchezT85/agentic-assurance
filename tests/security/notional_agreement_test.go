package security

import (
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
	"agentic-assurance/internal/policy"
)

// One order, one amount, three implementations.
//
// "What does this intent commit" is computed in three packages: authority decides whether
// the grant allows it, policy compares it against thresholds, and the cluster analysis
// counts it toward a parent intention. Each has its own copy of the rule, and each comment
// says it mirrors the others.
//
// Two of them guarded the case where price × quantity does not fit in the platform's
// representation; `intent.ClusterNotional` did not, and returned zero **determinately** —
// the largest order the platform could ever see, counted as contributing nothing to the
// heuristic that looks for one intention split into many. `money.NotionalOf` returns zero
// for exactly that case and says in its own comment that the caller treats zero as
// indeterminate.
//
// This pins the agreement rather than the arithmetic: if a fourth copy appears, or one of
// the three drifts, the disagreement fails here instead of showing up as a fleet metric
// nobody can reconcile.
func TestTheThreeNotionalRulesAgree(t *testing.T) {
	price := func(v string) *money.Amount {
		a := money.MustParse(v)
		return &a
	}
	quantity := func(v string) *money.Quantity {
		q, err := money.ParseQuantity(v)
		if err != nil {
			t.Fatalf("quantity %q: %v", v, err)
		}
		return &q
	}
	amount := func(v string) *money.Amount {
		a := money.MustParse(v)
		return &a
	}

	cases := []struct {
		name string
		in   intent.Intent
	}{
		{"a notional stated outright", intent.Intent{
			OrderType: intent.OrderMarket, Notional: amount("1000.5000")}},
		{"a limit order sized by quantity", intent.Intent{
			OrderType: intent.OrderLimit, Quantity: quantity("10"), LimitPrice: price("190.5000")}},
		{"a fraction that rounds up", intent.Intent{
			OrderType: intent.OrderLimit, Quantity: quantity("0.00000001"), LimitPrice: price("0.0001")}},
		{"a market order sized by quantity", intent.Intent{
			OrderType: intent.OrderMarket, Quantity: quantity("10")}},
		{"a stop order sized by quantity", intent.Intent{
			OrderType: intent.OrderStop, Quantity: quantity("10"), StopPrice: price("100")}},
		{"nothing at all", intent.Intent{OrderType: intent.OrderMarket}},
		{"a product too large to represent", intent.Intent{
			OrderType: intent.OrderLimit,
			Quantity:  quantity("40000000000"), LimitPrice: price("400000000000000")}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantAmount, wantOK := authority.EffectiveNotional(c.in)

			gotAmount, gotOK := intent.ClusterNotional(c.in)
			if gotAmount != wantAmount || gotOK != wantOK {
				t.Errorf("intent.ClusterNotional says (%s, %v) and authority says (%s, %v).\n\n"+
					"One order has one amount. Two components that disagree about it are "+
					"two different questions being asked about the same trade, and the "+
					"one that reads an unrepresentable order as a determinate zero is "+
					"the dangerous half.",
					gotAmount, gotOK, wantAmount, wantOK)
			}

			// Policy keeps its copy unexported, so what is pinned here is the behaviour
			// that copy exists for: a size-dependent rule met with an order of unknown
			// size fires when it denies and does not fire when it allows.
			//
			// The first version of this test asserted "policy saw an amount" and failed
			// on four cases — because a DENY rule fires deliberately when the size is
			// unknown, which is fail-closed and right. The probe was wrong, not the code,
			// and it is worth saying so: an audit that reports its own bad probe as a
			// finding is the failure these passes keep looking for.
			if !wantOK {
				if !policyDenies(t, c.in, denyRule) {
					t.Error("a DENY rule that depends on size did not fire against an " +
						"order whose size cannot be determined; every notional rule " +
						"would then be avoidable by omitting the notional")
				}
				if policyDenies(t, c.in, allowRule) {
					t.Error("an ALLOW rule fired for an order whose size is unknown")
				}
			}
		})
	}
}

const denyRule = `
version: 1
policy: pol_agreement
rules:
  - id: size_dependent_denial
    action: DENY
    when:
      notional_gte: 0
`

const allowRule = `
version: 1
policy: pol_agreement
rules:
  - id: size_dependent_permission
    action: ALLOW
    when:
      notional_gte: 0
`

// policyDenies reports whether a one-rule bundle denies this intent.
func policyDenies(t *testing.T, in intent.Intent, rule string) bool {
	t.Helper()

	source, err := policy.ParseSource([]byte(rule))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(source, "tenant_agreement", "bundle_agreement", at())
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	env := &intent.AgentExecutionEnvelope{TenantID: "tenant_agreement", Intent: in}
	return policy.Evaluate(bundle, env, at()).Action == policy.ActionDeny
}

// at is a fixed instant: none of this depends on the clock.
func at() time.Time { return time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC) }
