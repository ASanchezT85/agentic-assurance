package policy

import (
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// Policy thresholds, at the exact value the customer wrote.
//
// A rule is the customer's own sentence about their money. `notional_gt: 1000` and
// `notional_gte: 1000` differ by one order — the one of exactly 1000 — and which of them
// fires is the difference between a rule that catches the order the customer meant and one
// that lets it through. Every example in the suite sat comfortably above or below its
// threshold; none sat on it.
func TestThresholdsAtTheExactValue(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		clause string
		fires  bool
		why    string
	}{
		{"notional_gt: 1000", false, "greater than 1000 does not include 1000"},
		{"notional_gte: 1000", true, "at least 1000 includes 1000"},
		{"notional_lt: 1000", false, "less than 1000 does not include 1000"},
		{"notional_lte: 1000", true, "at most 1000 includes 1000"},
	}

	for _, c := range cases {
		t.Run(c.clause, func(t *testing.T) {
			source, err := ParseSource([]byte(fmt.Sprintf(`
version: 1
policy: pol_boundary
rules:
  - id: at_the_threshold
    action: DENY
    when:
      %s
`, c.clause)))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			bundle, err := Compile(source, "tenant_b", "bundle_b", at)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}

			amount := money.MustParse("1000")
			env := &intent.AgentExecutionEnvelope{
				TenantID: "tenant_b",
				Intent: intent.Intent{
					InstrumentID: "instr_us_equity_00206R102",
					AssetClass:   intent.AssetEquity, Side: intent.SideBuy,
					OrderType: intent.OrderMarket, Notional: &amount,
				},
			}

			d := Evaluate(bundle, env, at)
			fired := d.Action == ActionDeny
			if fired != c.fires {
				t.Errorf("%s fired=%v against a notional of exactly 1000; %s",
					c.clause, fired, c.why)
			}
		})
	}
}

// A requirement is satisfied at its own boundary: require_notional_lte 1000 permits 1000.
func TestARequirementIsSatisfiedAtItsBoundary(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	source, err := ParseSource([]byte(`
version: 1
policy: pol_boundary
rules:
  - id: ceiling
    action: DENY
    when:
      asset_class: EQUITY
    require:
      notional_lte: 1000
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := Compile(source, "tenant_b", "bundle_b", at)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}

	envelope := func(v string) *intent.AgentExecutionEnvelope {
		amount := money.MustParse(v)
		return &intent.AgentExecutionEnvelope{
			TenantID: "tenant_b",
			Intent: intent.Intent{
				InstrumentID: "instr_us_equity_00206R102",
				AssetClass:   intent.AssetEquity, Side: intent.SideBuy,
				OrderType: intent.OrderMarket, Notional: &amount,
			},
		}
	}

	if d := Evaluate(bundle, envelope("1000"), at); d.Action == ActionDeny {
		t.Error("an order of exactly the required ceiling was denied; require_notional_lte " +
			"1000 is satisfied by 1000")
	}
	if d := Evaluate(bundle, envelope("1000.0001"), at); d.Action != ActionDeny {
		t.Error("an order one ten-thousandth over the required ceiling was allowed")
	}
}
