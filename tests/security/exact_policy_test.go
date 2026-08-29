package security

import (
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
	"agentic-assurance/internal/policy"
)

// T3-MONEY-03: a policy threshold is compared exactly, on both sides.
//
// Policy notional thresholds were float64 while authority ceilings were exact, so one
// order was measured against two different questions. At a threshold of 1000.0001 the
// interesting cases are the values on either side of it and the value that is it — the
// places where "greater than" stops being obvious once a float is involved.

const exactPolicyAt = "2026-08-29T12:00:00Z"

func compiledRule(t *testing.T, operator, threshold string) *policy.Bundle {
	t.Helper()

	source := fmt.Sprintf(`
version: 1
policy: pol_exact
rules:
  - id: threshold
    action: DENY
    when:
      %s: %s
`, operator, threshold)

	at, err := time.Parse(time.RFC3339, exactPolicyAt)
	if err != nil {
		t.Fatalf("clock: %v", err)
	}
	src, err := policy.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("parse %s %s: %v", operator, threshold, err)
	}
	bundle, err := policy.Compile(src, "tenant_exact", "bundle_exact", at)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return bundle
}

func envelopeWithExactNotional(t *testing.T, literal string) *intent.AgentExecutionEnvelope {
	t.Helper()
	amount := money.MustParse(literal)
	return &intent.AgentExecutionEnvelope{
		EnvelopeID:    "env_exact",
		TenantID:      "tenant_exact",
		CorrelationID: "corr_exact",
		Intent: intent.Intent{
			InstrumentID: "instr_us_equity_00206R102",
			AssetClass:   intent.AssetEquity,
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     &amount,
			TimeInForce:  intent.TIFDay,
		},
	}
}

func TestPolicyThresholdsCompareExactly(t *testing.T) {
	const threshold = "1000.0001"

	// One ten-thousandth below the threshold, exactly on it, and one above.
	cases := []struct {
		operator string
		value    string
		wantDeny bool
	}{
		{"notional_gt", "1000.0000", false},
		{"notional_gt", "1000.0001", false}, // greater than, not equal to
		{"notional_gt", "1000.0002", true},

		{"notional_gte", "1000.0000", false},
		{"notional_gte", "1000.0001", true},
		{"notional_gte", "1000.0002", true},

		{"notional_lt", "1000.0000", true},
		{"notional_lt", "1000.0001", false},
		{"notional_lt", "1000.0002", false},

		{"notional_lte", "1000.0000", true},
		{"notional_lte", "1000.0001", true},
		{"notional_lte", "1000.0002", false},
	}

	at, err := time.Parse(time.RFC3339, exactPolicyAt)
	if err != nil {
		t.Fatalf("clock: %v", err)
	}

	for _, c := range cases {
		t.Run(c.operator+"_"+c.value, func(t *testing.T) {
			bundle := compiledRule(t, c.operator, threshold)
			decision := policy.Evaluate(bundle, envelopeWithExactNotional(t, c.value), at)

			denied := decision.Action == policy.ActionDeny
			if denied != c.wantDeny {
				t.Errorf("%s %s against %s: denied=%v, want %v. One ten-thousandth is a "+
					"real amount at this scale, and a threshold that cannot resolve it "+
					"is a threshold nobody can predict.",
					c.value, c.operator, threshold, denied, c.wantDeny)
			}
		})
	}
}

// T3-MONEY-04: quantity times price, with the rounding rule stated and asserted.
//
// A price at four decimal places times a quantity at eight lands between monetary units
// far more often than not. The rule is to round up, away from zero: a ceiling must count
// at least what an order can cost, and rounding down would let a sequence of orders each
// shave a fraction off what the grant is charged.
func TestQuantityTimesPriceRoundsUp(t *testing.T) {
	cases := []struct {
		price    string
		quantity string
		want     string
		why      string
	}{
		{"10", "3", "30", "exact, no rounding needed"},
		{"0.3333", "3", "0.9999", "exact at the scale"},
		{"1234.5678", "3.3333333", "4115.2260", "lands between units and rounds up"},
		{"0.0001", "0.5", "0.0001", "half a unit costs a whole one: a ceiling counts what it can cost"},
		{"0.0001", "0.00000001", "0.0001", "a vanishing fraction still costs the smallest unit"},
		{"100.5000", "2.5", "251.2500", "exact"},
		{"-10", "3", "-30", "away from zero in both directions"},
	}

	for _, c := range cases {
		price := money.MustParse(c.price)
		quantity := money.MustParseQuantity(c.quantity)
		got := money.NotionalOf(price, quantity)
		if got != money.MustParse(c.want) {
			t.Errorf("%s x %s = %s, want %s (%s)", c.price, c.quantity, got, c.want, c.why)
		}
	}
}

// A product too large to represent is refused rather than wrapped.
//
// An int64 of ten-thousandths reaches about 922 trillion. A quantity and price whose
// product passes that must not become a small number: the caller treats an
// indeterminate notional as a denial, and a wrapped one would authorize an enormous
// order as a trivial one.
func TestAnUnrepresentableProductIsNotSilentlyWrapped(t *testing.T) {
	price := money.MustParse("900000000000")
	quantity := money.MustParseQuantity("1000000")

	if got := money.NotionalOf(price, quantity); got != 0 {
		t.Errorf("%s x %s produced %s; a product beyond the supported range must be "+
			"reported as indeterminate rather than wrapped into a representable one",
			price, quantity, got)
	}
}
