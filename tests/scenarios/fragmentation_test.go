// Package scenarios holds the executable form of the stress library in spec
// section 41.
//
// Phase 7 delivers S06 and S07, which are its exit criteria. They run against
// constructed intent streams rather than the Digital Twin: the reconstruction is
// deterministic, so a hand-built stream exercises it exactly. The simulator-driven
// versions, with populations and market state, arrive in Phase 12.
package scenarios

import (
	"context"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
)

var origin = time.Date(2026, 8, 27, 14, 32, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

type child struct {
	envelopeID string
	agentID    string
	strategyID string
	grantID    string
	notional   float64
	offset     time.Duration
	side       intent.Side
	instrument string
}

func envelope(c child) *intent.AgentExecutionEnvelope {
	side := c.side
	if side == "" {
		side = intent.SideBuy
	}
	instrument := c.instrument
	if instrument == "" {
		instrument = "instr_us_equity_00206R102"
	}
	grant := c.grantID
	if grant == "" {
		grant = "grant_5521"
	}

	return &intent.AgentExecutionEnvelope{
		SchemaVersion:    intent.SchemaVersion,
		EnvelopeID:       c.envelopeID,
		IdempotencyKey:   "idem_" + c.envelopeID,
		CorrelationID:    "corr_" + c.envelopeID,
		ReceivedAt:       origin.Add(c.offset),
		TenantID:         "tenant_acme",
		AuthorityGrantID: grant,
		Principal:        intent.Principal{PrincipalID: "principal_7781", AccountID: "account_4410"},
		Agent:            intent.Agent{AgentID: c.agentID},
		// Structurally signed. These scenarios are about clustering and policy rather
		// than cryptography, and an executable envelope carries a signature now.
		Signature: intent.Signature{
			Algorithm: "Ed25519", KeyID: "agent-key-01", Value: "aa",
		},
		Lineage: intent.Lineage{StrategyID: c.strategyID},
		Context: intent.Context{MarketSnapshotID: "ms_41"},
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: instrument,
			Side:         side,
			OrderType:    intent.OrderMarket,
			Notional:     f(c.notional),
			TimeInForce:  intent.TIFDay,
		},
	}
}

// S06 — Order fragmentation.
//
// An agent splits a 20,000 intention into five 4,000 orders, each comfortably under
// a 5,000 per-order ceiling. Spec section 66 step 9 walks exactly this.
//
// Expected: the parent intent is reconstructed, its effective notional exceeds the
// rule, and detection does not depend on any single request breaching the limit.
func TestS06_OrderFragmentation(t *testing.T) {
	const perOrderLimit = 5000.0

	children := []child{
		{envelopeID: "env_1", agentID: "agent_momentum_03", strategyID: "strategy_mom_v3", notional: 4000, offset: 0},
		{envelopeID: "env_2", agentID: "agent_momentum_03", strategyID: "strategy_mom_v3", notional: 4000, offset: 2 * time.Second},
		{envelopeID: "env_3", agentID: "agent_momentum_03", strategyID: "strategy_mom_v3", notional: 4000, offset: 5 * time.Second},
		{envelopeID: "env_4", agentID: "agent_momentum_03", strategyID: "strategy_mom_v3", notional: 4000, offset: 8 * time.Second},
		{envelopeID: "env_5", agentID: "agent_momentum_03", strategyID: "strategy_mom_v3", notional: 4000, offset: 11 * time.Second},
	}

	envelopes := make([]*intent.AgentExecutionEnvelope, 0, len(children))
	for _, c := range children {
		envelopes = append(envelopes, envelope(c))
	}

	// Every individual order passes authority. That is the premise of the scenario:
	// per-request limits alone see nothing wrong.
	grant := fragmentationGrant(perOrderLimit)
	for _, e := range envelopes {
		if d := authority.Evaluate(context.Background(), e, grant, nil, origin); !d.Allowed {
			t.Fatalf("%s was denied per-request (%s); the scenario requires each order "+
				"to pass on its own", e.EnvelopeID, d.Code)
		}
	}

	parents := intent.Reconstruct(envelopes, intent.DefaultClusterConfig)
	if len(parents) != 1 {
		t.Fatalf("reconstructed %d parent intents, want 1", len(parents))
	}
	p := parents[0]

	if p.ChildCount != 5 {
		t.Errorf("child_count = %d, want 5", p.ChildCount)
	}
	if p.GrossNotional != 20000 {
		t.Errorf("gross_notional = %.2f, want 20000", p.GrossNotional)
	}
	if !p.Fragmented(perOrderLimit) {
		t.Errorf("a 20,000 intention split into 4,000 pieces was not flagged as "+
			"fragmented against a %.0f ceiling", perOrderLimit)
	}
	if !p.ExposureComplete() {
		t.Error("the gross total does not account for every child")
	}
	if p.TimeSpan != 11*time.Second {
		t.Errorf("time_span = %s, want 11s", p.TimeSpan)
	}

	// The reconstruction reports what corroborated it rather than only a number.
	if p.Confidence <= 0 || p.Confidence > 1 {
		t.Errorf("confidence = %v, want a fraction", p.Confidence)
	}
	if len(p.Signals) == 0 {
		t.Error("no corroborating signals were reported; the number alone is not explainable")
	}

	// And it does not claim more than it saw: one agent, one strategy, one grant.
	if p.CrossAgent() {
		t.Error("a single-agent cluster was reported as cross-agent")
	}
}

// Orders far enough apart are not one intention. Without this the engine would
// eventually merge a day's trading into a single "fragmented" intent.
func TestS06_SeparatedOrdersAreNotOneIntent(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000, offset: 0}),
		envelope(child{envelopeID: "env_2", agentID: "agent_a", notional: 4000, offset: 2 * time.Second}),
		// Well past the window.
		envelope(child{envelopeID: "env_3", agentID: "agent_a", notional: 4000, offset: 10 * time.Minute}),
		envelope(child{envelopeID: "env_4", agentID: "agent_a", notional: 4000, offset: 10*time.Minute + 2*time.Second}),
	}

	parents := intent.Reconstruct(envelopes, intent.DefaultClusterConfig)
	if len(parents) != 2 {
		t.Fatalf("reconstructed %d parent intents, want 2 separate clusters", len(parents))
	}
	for _, p := range parents {
		if p.GrossNotional != 8000 {
			t.Errorf("cluster gross = %.2f, want 8000", p.GrossNotional)
		}
	}
}

// A single order is an order. Calling it a fragmented intention would flood any
// reviewer with noise.
func TestS06_ASingleOrderIsNotAParentIntent(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000}),
	}
	if parents := intent.Reconstruct(envelopes, intent.DefaultClusterConfig); len(parents) != 0 {
		t.Errorf("a lone order produced %d parent intents", len(parents))
	}
}

// Market orders sized by quantity have no determinable notional. They must be
// counted as unaccounted rather than silently dropped, or fragmenting behind market
// orders would defeat the whole engine.
func TestS06_UndeterminableChildrenAreCountedNotDropped(t *testing.T) {
	determinable := envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000})

	hidden := envelope(child{envelopeID: "env_2", agentID: "agent_a", offset: time.Second})
	hidden.Intent.Notional = nil
	hidden.Intent.Quantity = f(100000)

	parents := intent.Reconstruct([]*intent.AgentExecutionEnvelope{determinable, hidden},
		intent.DefaultClusterConfig)
	if len(parents) != 1 {
		t.Fatalf("reconstructed %d parent intents, want 1", len(parents))
	}
	p := parents[0]

	if p.ChildCount != 2 {
		t.Errorf("child_count = %d, want 2", p.ChildCount)
	}
	if p.IndeterminateChildren != 1 {
		t.Errorf("indeterminate_children = %d, want 1", p.IndeterminateChildren)
	}
	if p.ExposureComplete() {
		t.Error("the reconstruction reported a complete exposure while a child's size " +
			"was unknown; the real total is at least the reported figure and possibly more")
	}
}

// Reconstruction is deterministic: the same intents in any order produce the same
// parent intent, with the same identifier.
func TestS06_ReconstructionIsDeterministic(t *testing.T) {
	build := func() []*intent.AgentExecutionEnvelope {
		return []*intent.AgentExecutionEnvelope{
			envelope(child{envelopeID: "env_1", agentID: "agent_a", notional: 4000, offset: 0}),
			envelope(child{envelopeID: "env_2", agentID: "agent_a", notional: 4000, offset: time.Second}),
			envelope(child{envelopeID: "env_3", agentID: "agent_a", notional: 4000, offset: 2 * time.Second}),
		}
	}

	forward := intent.Reconstruct(build(), intent.DefaultClusterConfig)

	shuffled := build()
	shuffled[0], shuffled[2] = shuffled[2], shuffled[0]
	backward := intent.Reconstruct(shuffled, intent.DefaultClusterConfig)

	if len(forward) != 1 || len(backward) != 1 {
		t.Fatalf("expected one cluster each, got %d and %d", len(forward), len(backward))
	}
	if forward[0].ParentIntentID != backward[0].ParentIntentID {
		t.Errorf("input order changed the parent intent id:\n  %s\n  %s",
			forward[0].ParentIntentID, backward[0].ParentIntentID)
	}
	if forward[0].GrossNotional != backward[0].GrossNotional {
		t.Error("input order changed the reconstructed exposure")
	}
}

func fragmentationGrant(perOrderLimit float64) *authority.Grant {
	return &authority.Grant{
		GrantID:             "grant_5521",
		TenantID:            "tenant_acme",
		PrincipalID:         "principal_7781",
		AccountID:           "account_4410",
		AgentID:             "agent_momentum_03",
		IssuedAt:            origin.Add(-24 * time.Hour),
		ValidFrom:           origin.Add(-time.Hour),
		ValidUntil:          origin.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		Limits:              authority.Limits{PerOrderNotional: perOrderLimit},
		Status:              authority.StatusActive,
	}
}
