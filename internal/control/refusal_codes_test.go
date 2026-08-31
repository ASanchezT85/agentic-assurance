package control

import (
	"testing"
	"time"

	"agentic-assurance/internal/intent"
)

// NO_CONTROL_APPLIES: the answer when nothing in force touches this order.
//
// The allow path had never been executed by a test. It is the one an order takes on every
// ordinary day, and a decision that carries no code — or the wrong one — is what an
// operator reads when they ask why an order went through.
func TestNothingInForceAllowsWithACode(t *testing.T) {
	env := &intent.AgentExecutionEnvelope{
		TenantID: "tenant_1",
		Agent:    intent.Agent{AgentID: "agent_1"},
		Intent: intent.Intent{
			InstrumentID: "instr_us_equity_00206R102",
			AssetClass:   intent.AssetEquity,
			Side:         intent.SideBuy,
		},
	}

	d := Evaluate(nil, env, time.Now().UTC())
	if !d.Allowed {
		t.Fatalf("an order was refused with no control in force: %s", d.Code)
	}
	if d.Code != "NO_CONTROL_APPLIES" {
		t.Errorf("allowed with code %q; the decision has to say why it allowed, or an "+
			"operator reading the record cannot tell an untouched order from an "+
			"unevaluated one", d.Code)
	}
}
