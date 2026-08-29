package security

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// The production authority engine and the Digital Twin must agree on the one rule they
// both implement.
//
// simulator/assurance/engine.py says plainly that it is not the production engine and
// that its rules can drift. Stating a risk is not detecting one. Both engines read
// tests/fixtures/per_order_limit_cases.json, and the Python half of this check is
// simulator/test_engine_agreement.py.
//
// If they ever disagree, one of the two tests fails and names the case. Without them
// the first divergence would surface as a scenario that passed in the twin and a fleet
// that behaved differently in production, which is the most expensive place to find it.

type limitCase struct {
	Name             string  `json:"name"`
	PerOrderNotional float64 `json:"per_order_notional"`
	Quantity         float64 `json:"quantity"`
	ReferencePrice   float64 `json:"reference_price"`
	Allowed          bool    `json:"allowed"`
	Code             string  `json:"code"`
}

func TestProductionEngineMatchesTheSharedLimitContract(t *testing.T) {
	raw, err := os.ReadFile("../fixtures/per_order_limit_cases.json")
	if err != nil {
		t.Fatalf("read the shared fixture: %v", err)
	}

	var document struct {
		Cases []limitCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode the shared fixture: %v", err)
	}
	if len(document.Cases) == 0 {
		t.Fatal("the shared fixture has no cases; an empty contract agrees with anything")
	}

	at := time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)

	for _, c := range document.Cases {
		t.Run(c.Name, func(t *testing.T) {
			// The twin computes notional as quantity times a reference price. The
			// production engine takes an explicit notional, because it will not
			// invent a price it does not have (ADR-019). The contract is about the
			// limit comparison, so the notional is computed the same way here and
			// the difference in how each engine obtains one stays out of it.
			// Exactly, through the same rule production uses. A float multiply here
			// produced values like 5000.010000000001, which the platform refuses as
			// more precision than it keeps — the test would then be comparing a
			// refusal about precision against a contract about limits.
			notional := money.Notional(mustAmount(t, c.ReferencePrice), c.Quantity).Float()

			grant := &authority.Grant{
				GrantID:             "grant_contract",
				TenantID:            "tenant_contract",
				PrincipalID:         "prin_contract",
				AccountID:           "acct_contract",
				AgentID:             "agent_contract",
				IssuedAt:            at.Add(-time.Hour),
				ValidFrom:           at.Add(-time.Hour),
				ValidUntil:          at.Add(time.Hour),
				AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
				AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
				// The twin's fixture carries a float; the platform decides in exact
				// money, so it is converted here rather than compared as one.
				Limits: authority.Limits{PerOrderNotional: mustAmount(t, c.PerOrderNotional)},
				Status: authority.StatusActive,
			}

			env := &intent.AgentExecutionEnvelope{
				TenantID:         "tenant_contract",
				AuthorityGrantID: "grant_contract",
				ReceivedAt:       at,
				Principal: intent.Principal{
					PrincipalID: "prin_contract", AccountID: "acct_contract",
				},
				Agent: intent.Agent{AgentID: "agent_contract"},
				Intent: intent.Intent{
					InstrumentID: "instr_us_equity_00206R102",
					AssetClass:   intent.AssetEquity,
					Side:         intent.SideBuy,
					OrderType:    intent.OrderMarket,
					Notional:     &notional,
					TimeInForce:  intent.TIFDay,
				},
			}

			decision := authority.Evaluate(context.Background(), env, grant, nil, at)

			if decision.Allowed != c.Allowed {
				verb, want := "refused", "allow"
				if decision.Allowed {
					verb, want = "allowed", "refuse"
				}
				t.Fatalf("the production engine %s an intent the shared contract says "+
					"it should %s (%g x %g against a limit of %g), code %s: %s. "+
					"Either the engines have drifted apart, or the contract is wrong.",
					verb, want, c.Quantity, c.ReferencePrice, c.PerOrderNotional,
					decision.Code, decision.Reason)
			}

			if c.Code != "" && decision.Code != c.Code {
				t.Errorf("code = %q, contract says %q. The two engines must refuse for "+
					"the same stated reason, or an operator reading a scenario cannot "+
					"map it onto production behaviour.", decision.Code, c.Code)
			}
		})
	}
}

// mustAmount converts a fixture value into the exact representation authority decides
// in, failing loudly if the fixture carries more precision than the platform keeps.
func mustAmount(t *testing.T, f float64) money.Amount {
	t.Helper()
	amount, err := money.FromFloat(f)
	if err != nil {
		t.Fatalf("fixture value %v is not an exact amount: %v", f, err)
	}
	return amount
}
