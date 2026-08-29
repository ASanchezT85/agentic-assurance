package scenarios

import (
	"context"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// S07 — Cross-agent accumulation.
//
// Three agents under one principal each buy the same instrument. Every order is
// within its own agent's grant. The combined exposure is not, and spec section 21
// requires the principal-level view to see it.
//
// Spec section 66 step 10 walks this scenario.

func TestS07_CrossAgentAccumulation(t *testing.T) {
	// The example from spec section 21, verbatim.
	envelopes := []*intent.AgentExecutionEnvelope{
		envelope(child{envelopeID: "env_a", agentID: "agent_a", strategyID: "strategy_alpha",
			grantID: "grant_a", notional: 4000, offset: 0}),
		envelope(child{envelopeID: "env_b", agentID: "agent_b", strategyID: "strategy_beta",
			grantID: "grant_b", notional: 4500, offset: 3 * time.Second}),
		envelope(child{envelopeID: "env_c", agentID: "agent_c", strategyID: "strategy_gamma",
			grantID: "grant_c", notional: 3000, offset: 6 * time.Second}),
	}

	// Each agent's own grant permits its own order. That is the premise: per-agent
	// authority sees nothing wrong.
	for _, e := range envelopes {
		g := perAgentGrant(e.Agent.AgentID, e.AuthorityGrantID, money.MustParse("5000"))
		if d := authority.Evaluate(context.Background(), e, g, nil, origin); !d.Allowed {
			t.Fatalf("%s was denied under its own grant (%s); the scenario requires each "+
				"agent to be individually within its limits", e.EnvelopeID, d.Code)
		}
	}

	parents := intent.Reconstruct(envelopes, intent.DefaultClusterConfig)
	if len(parents) != 1 {
		t.Fatalf("reconstructed %d parent intents, want 1 principal-level view", len(parents))
	}
	p := parents[0]

	// The number spec section 21 names.
	if p.GrossNotional != money.MustParse("11500") {
		t.Errorf("effective exposure = %s, want 11500", p.GrossNotional)
	}
	if p.NetNotional != money.MustParse("11500") {
		t.Errorf("net exposure = %s, want 11500 for three same-side buys", p.NetNotional)
	}
	if !p.CrossAgent() {
		t.Fatal("three agents under one principal were not reported as cross-agent (INV-002, spec 21)")
	}
	if p.AgentCount != 3 {
		t.Errorf("agent_count = %d, want 3", p.AgentCount)
	}
	if p.PrincipalID != "principal_7781" {
		t.Errorf("principal = %q", p.PrincipalID)
	}

	// The combined exposure breaches a principal-level ceiling that no individual
	// order came close to.
	// Exact, and named as an amount rather than left as an untyped constant. Against
	// money.Amount a bare 10000.0 means ten thousand ten-thousandths — one currency
	// unit — and the assertion would pass for the wrong reason.
	principalCeiling := money.MustParse("10000")
	if p.GrossNotional <= principalCeiling {
		t.Fatal("the scenario is misconfigured: the combined exposure does not breach the ceiling")
	}

	// Confidence is lower than the single-agent case, and honestly so: three agents,
	// three strategies and three grants are weaker corroboration that this was one
	// intention.
	if p.Confidence >= 1 {
		t.Errorf("confidence = %v; a cross-agent, cross-strategy cluster should not "+
			"claim full corroboration", p.Confidence)
	}
	for _, s := range p.Signals {
		if s == intent.SignalSameAgent || s == intent.SignalSameStrategy || s == intent.SignalSameGrant {
			t.Errorf("signal %s was reported for a cluster that has three of each", s)
		}
	}
}

// Opposite sides do not accumulate. Three buys and three sells under one principal
// are not one intention, and merging them would report exposure that nobody has.
func TestS07_OppositeSidesDoNotAccumulate(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		envelope(child{envelopeID: "env_buy_1", agentID: "agent_a", notional: 4000, side: intent.SideBuy}),
		envelope(child{envelopeID: "env_buy_2", agentID: "agent_b", notional: 4000, side: intent.SideBuy, offset: time.Second}),
		envelope(child{envelopeID: "env_sell_1", agentID: "agent_c", notional: 4000, side: intent.SideSell, offset: 2 * time.Second}),
		envelope(child{envelopeID: "env_sell_2", agentID: "agent_d", notional: 4000, side: intent.SideSell, offset: 3 * time.Second}),
	}

	parents := intent.Reconstruct(envelopes, intent.DefaultClusterConfig)
	if len(parents) != 2 {
		t.Fatalf("reconstructed %d parent intents, want one per side", len(parents))
	}
	for _, p := range parents {
		if p.GrossNotional != money.MustParse("8000") {
			t.Errorf("side %s: gross = %s, want 8000", p.Side, p.GrossNotional)
		}
		if p.Side == intent.SideSell && p.NetNotional != money.MustParse("-8000") {
			t.Errorf("sell cluster net = %s; spec section 23 requires the direction "+
				"to survive", p.NetNotional)
		}
	}
}

// Different principals never merge, whatever else they share. Combining two
// customers' exposure would be a cross-tenant style error inside one tenant.
func TestS07_DifferentPrincipalsNeverMerge(t *testing.T) {
	first := envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000})
	second := envelope(child{envelopeID: "env_2", agentID: "agent_b", notional: 4000, offset: time.Second})
	second.Principal.PrincipalID = "principal_someone_else"

	parents := intent.Reconstruct([]*intent.AgentExecutionEnvelope{first, second},
		intent.DefaultClusterConfig)
	if len(parents) != 0 {
		t.Fatalf("two principals were merged into %d parent intents", len(parents))
	}
}

// Different instruments never merge either. Exposure to one name is not exposure to
// another, however correlated a human might think they are.
func TestS07_DifferentInstrumentsNeverMerge(t *testing.T) {
	first := envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000})
	second := envelope(child{envelopeID: "env_2", agentID: "agent_b", notional: 4000,
		offset: time.Second, instrument: "instr_us_equity_88160R101"})

	parents := intent.Reconstruct([]*intent.AgentExecutionEnvelope{first, second},
		intent.DefaultClusterConfig)
	if len(parents) != 0 {
		t.Fatalf("two instruments were merged into %d parent intents", len(parents))
	}
}

// Tenants never merge. This is INV-007 expressed in the reconstruction engine.
func TestS07_TenantsNeverMerge(t *testing.T) {
	first := envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000})
	second := envelope(child{envelopeID: "env_2", agentID: "agent_a", notional: 4000, offset: time.Second})
	second.TenantID = "tenant_globex"

	parents := intent.Reconstruct([]*intent.AgentExecutionEnvelope{first, second},
		intent.DefaultClusterConfig)
	if len(parents) != 0 {
		t.Fatalf("two tenants were merged into %d parent intents (INV-007)", len(parents))
	}
}

func perAgentGrant(agentID, grantID string, perOrderLimit money.Amount) *authority.Grant {
	return &authority.Grant{
		GrantID:             grantID,
		TenantID:            "tenant_acme",
		PrincipalID:         "principal_7781",
		AccountID:           "account_4410",
		AgentID:             agentID,
		IssuedAt:            origin.Add(-24 * time.Hour),
		ValidFrom:           origin.Add(-time.Hour),
		ValidUntil:          origin.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		Limits:              authority.Limits{PerOrderNotional: perOrderLimit},
		Status:              authority.StatusActive,
	}
}
