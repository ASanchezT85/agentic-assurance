package scenarios

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// Scenarios that exercise the enforcement plane.
//
// These run against the real gateway components. A scenario that passed against a
// stand-in would prove nothing about what the platform does.

// S03 — Stale market feed.
//
// Agents operate from stale pricing. Expected: the stale-data state is visible, and
// policy can deny or require approval.
func TestS03_StaleMarketFeed(t *testing.T) {
	staleAt := origin.Add(-45 * time.Minute)

	fresh := envelope(child{envelopeID: "env_fresh", agentID: "agent_a", notional: 4000})
	fresh.Dependencies = []intent.Dependency{{
		Type: intent.DependencyMarketData, ID: "feed_a",
		Verification: intent.VerificationVerified, ObservedAt: origin, EvidenceRef: "ev_1",
	}}

	stale := envelope(child{envelopeID: "env_stale", agentID: "agent_b", notional: 4000,
		offset: time.Second})
	stale.Dependencies = []intent.Dependency{{
		Type: intent.DependencyMarketData, ID: "feed_a",
		Verification: intent.VerificationVerified, ObservedAt: staleAt, EvidenceRef: "ev_2",
	}}

	// Both envelopes are structurally valid. Staleness is not a validation failure:
	// an old observation is a fact about the data, not a malformed field, and spec
	// section 17 says to apply explicit policy rather than reject outright.
	for _, e := range []*intent.AgentExecutionEnvelope{fresh, stale} {
		if err := e.Validate(); err != nil {
			t.Fatalf("%s should be structurally valid: %v", e.EnvelopeID, err)
		}
	}

	// The state is visible: the observation time survives validation unchanged, so
	// anything downstream can compute the age.
	freshAge := origin.Sub(fresh.Dependencies[0].ObservedAt)
	staleAge := origin.Sub(stale.Dependencies[0].ObservedAt)

	if freshAge > time.Minute {
		t.Errorf("the fresh dependency is %s old", freshAge)
	}
	if staleAge < 30*time.Minute {
		t.Fatalf("the stale dependency is only %s old; the scenario needs it stale", staleAge)
	}

	// And the platform never silently assumes the price is current. The observation
	// time is preserved to the second rather than normalised away.
	if !stale.Dependencies[0].ObservedAt.Equal(staleAt.UTC()) {
		t.Errorf("the observation time was altered: %s", stale.Dependencies[0].ObservedAt)
	}

	// Policy can act on it. A rule that requires approval above a notional applies
	// to the stale order exactly as it would to a fresh one; what the scenario
	// establishes is that the staleness is available to be acted on rather than
	// discarded on the way in.
	bundle := activeBundle(t, `
version: 1
policy: stale_feed_guard
rules:
  - id: LARGE_ORDER_APPROVAL
    when:
      notional_gt: 2500
    action: REQUIRE_APPROVAL
`)
	decision := policy.Evaluate(bundle, stale, origin)
	if decision.Action != policy.ActionRequireApproval {
		t.Errorf("action = %s; policy must be able to gate an order placed on stale data",
			decision.Action)
	}
	if decision.BundleID == "" {
		t.Error("the decision does not record which bundle produced it")
	}
}

// S05 — Retry storm.
//
// Network timeouts cause repeated submissions. Expected: idempotency prevents
// duplicate broker execution, and the unknown execution state reconciles before any
// retry.
func TestS05_RetryStorm(t *testing.T) {
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return origin })

	svc := &execution.Service{
		Broker: fake,
		Store:  execution.NewMemoryStore(),
		Cache:  execution.NewMemoryCache(),
		Now:    func() time.Time { return origin },
	}

	// The venue receives the order and the response is lost. Every agent in a retry
	// storm is in exactly this position: no answer, so it asks again.
	fake.InjectFault("coid_storm", fakebroker.FaultTimeoutAfterReceipt)

	env := envelope(child{envelopeID: "env_storm", agentID: "agent_a", notional: 4000})
	env.IdempotencyKey = "storm"
	req := broker.OrderRequest{
		ClientOrderID: "coid_storm",
		TenantID:      "tenant_acme",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      q(100),
		LimitPrice:    f(50),
		TimeInForce:   intent.TIFDay,
	}

	// A storm: fifty retries of one intent.
	var outcomes []execution.Outcome
	for i := 0; i < 50; i++ {
		got, err := svc.Submit(context.Background(), env, req)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		outcomes = append(outcomes, got)
	}

	if n := fake.Submissions("coid_storm"); n != 1 {
		t.Fatalf("%d submissions reached the venue during a fifty-retry storm; "+
			"idempotency must prevent every duplicate (INV-004)", n)
	}

	// Reconciliation happened before anything was retried: the first call already
	// resolved the unknown state by asking the venue.
	if outcomes[0].State != broker.StateAccepted {
		t.Errorf("the first outcome was %s; an ambiguous timeout must reconcile rather "+
			"than stay unresolved when the venue can answer", outcomes[0].State)
	}
	for i, got := range outcomes[1:] {
		if !got.Replayed {
			t.Errorf("retry %d was not served from the record", i+1)
		}
		if got.State != outcomes[0].State {
			t.Errorf("retry %d returned %s, the first returned %s; a duplicate must "+
				"return the prior outcome (spec section 17)", i+1, got.State, outcomes[0].State)
		}
	}
}

// S09 — Policy regression.
//
// A new policy configuration introduces unsafe behaviour. Expected: pre-production
// simulation surfaces the impact, and staged rollout prevents silent global
// activation.
func TestS09_PolicyRegression(t *testing.T) {
	safe := compileBundle(t, `
version: 1
policy: retail_standard
rules:
  - id: ORDER_MAX_NOTIONAL
    when:
      asset_class: EQUITY
    require:
      notional_lte: 5000
    action: DENY
`)

	// The regression: someone raises the ceiling a hundredfold.
	regressed := compileBundle(t, `
version: 2
policy: retail_standard
rules:
  - id: ORDER_MAX_NOTIONAL
    when:
      asset_class: EQUITY
    require:
      notional_lte: 500000
    action: DENY
`)

	// Pre-production simulation: run the same traffic through both and compare. This
	// is what surfaces the impact before anything is activated.
	oversized := envelope(child{envelopeID: "env_big", agentID: "agent_a", notional: 250000})

	before := policy.Evaluate(safe, oversized, origin)
	after := policy.Evaluate(regressed, oversized, origin)

	if before.Action != policy.ActionDeny {
		t.Fatalf("precondition: the safe bundle should deny a 250,000 order, got %s", before.Action)
	}
	if after.Action == policy.ActionDeny {
		t.Fatal("precondition: the regressed bundle should allow it")
	}
	if before.Action == after.Action {
		t.Error("the simulation did not surface any difference between the bundles")
	}

	// Staged rollout: the regressed bundle cannot reach production without walking
	// the whole pipeline, and until it does it is not enforcing.
	if regressed.Enforcing() {
		t.Fatal("a freshly compiled bundle reports itself as enforcing")
	}
	if err := regressed.Transition(policy.StatusActive, origin, "someone-in-a-hurry"); err == nil {
		t.Error("the regressed bundle jumped straight to ACTIVE; staged rollout must " +
			"prevent silent global activation (INV-010)")
	}

	// Even walking the pipeline, every stage before ACTIVE is non-enforcing, so the
	// impact is observable in shadow before anything binds.
	signBundleFor(t, regressed)
	for _, stage := range []policy.Status{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary} {
		if err := regressed.Transition(stage, origin, "release-engineer"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
		if regressed.Enforcing() {
			t.Errorf("a bundle in %s reported itself as enforcing production", stage)
		}
	}

	// And rollback is available and audited at every point.
	if err := regressed.Rollback(origin, "operator", "simulation showed the ceiling was wrong"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if regressed.Activation.RollbackReason == "" {
		t.Error("the rollback was not audited")
	}
}

// S10 — Intelligence outage.
//
// Cloud intelligence disappears during high activity. Expected: hard local
// enforcement continues, and production does not depend on cloud intelligence.
func TestS10_IntelligenceOutage(t *testing.T) {
	bundle := activeBundle(t, `
version: 1
policy: retail_standard
rules:
  - id: ORDER_MAX_NOTIONAL
    when:
      asset_class: EQUITY
    require:
      notional_lte: 5000
    action: DENY
`)

	grant := fragmentationGrant(5000)

	// Everything outside the process is gone: no fleet engine, no ClickHouse, no
	// NATS, no usage backend, no market data. The arguments below are the whole
	// dependency surface, and every one of them is nil.
	oversized := envelope(child{envelopeID: "env_out", agentID: "agent_momentum_03",
		notional: 250000})

	authDecision := authority.Evaluate(context.Background(), oversized, grant, nil, origin)
	if authDecision.Allowed {
		t.Error("authority allowed an order over its per-order limit with every backend " +
			"unavailable (INV-005)")
	}

	policyDecision := policy.Evaluate(bundle, oversized, origin)
	if policyDecision.Action != policy.ActionDeny {
		t.Errorf("policy returned %s during an intelligence outage; hard limits must "+
			"still bind (INV-005)", policyDecision.Action)
	}

	// And a legitimate order still passes: an outage must not become a blanket
	// denial either, or the fail-safe would be an outage of its own.
	small := envelope(child{envelopeID: "env_small", agentID: "agent_momentum_03",
		notional: 1000})
	if d := authority.Evaluate(context.Background(), small, grant, nil, origin); !d.Allowed {
		t.Errorf("a compliant order was denied during an outage: %s", d.Code)
	}
	if d := policy.Evaluate(bundle, small, origin); d.Action == policy.ActionDeny {
		t.Errorf("policy denied a compliant order during an outage: %s", d.Reason)
	}

	// High activity changes nothing: enforcement is per-envelope and stateless.
	for i := 0; i < 500; i++ {
		e := envelope(child{envelopeID: "env_load_" + itoa(i), agentID: "agent_momentum_03",
			notional: 250000})
		if policy.Evaluate(bundle, e, origin).Action != policy.ActionDeny {
			t.Fatalf("enforcement degraded under load at request %d", i)
		}
	}
}

// S11 — Agent credential compromise.
//
// An unauthorized workload attempts execution. Expected: identity and attestation
// fail, the request is denied, and there is security incident evidence.
func TestS11_AgentCredentialCompromise(t *testing.T) {
	// The attacker presents nothing verifiable and claims to be an attested
	// workload. That claim is the compromise: the SPIFFE ID is real, and the
	// certificate proving it is not.
	verifier := &identity.Verifier{
		TrustDomain: "acme.example",
		Bundle:      emptyPool(),
	}

	established := verifier.Resolve(identity.Presented{})
	if established.Level != intent.AttestationA0 {
		t.Fatalf("level = %s; nothing was presented", established.Level)
	}

	// The envelope claims A2. The evidence establishes A0.
	if err := identity.CheckClaim(intent.AttestationA2, established); err == nil {
		t.Fatal("an agent claimed an attested identity it could not demonstrate, and " +
			"the claim was accepted (INV-001)")
	}

	// And an unauthenticated workload cannot create an executable order at all.
	if err := identity.RequireExecutable(established); err == nil {
		t.Fatal("an unauthenticated workload was allowed to create an executable order (INV-001)")
	}

	// A stolen grant does not help either: the grant is bound to an agent, and the
	// compromised workload is not that agent.
	grant := fragmentationGrant(5000)
	stolen := envelope(child{envelopeID: "env_stolen", agentID: "agent_impostor", notional: 1000})

	decision := authority.Evaluate(context.Background(), stolen, grant, nil, origin)
	if decision.Allowed {
		t.Fatal("a stolen grant authorized an order for a different agent")
	}
	if decision.Code != "GRANT_WRONG_AGENT" {
		t.Errorf("code = %s, want GRANT_WRONG_AGENT", decision.Code)
	}

	// The denial is recordable as security evidence: it names the agent, the grant
	// and the reason, which is what an investigation needs.
	if decision.GrantID == "" || decision.Reason == "" {
		t.Errorf("the denial is not investigable: %+v", decision)
	}
	if decision.EvaluatedAt.IsZero() {
		t.Error("the denial has no timestamp")
	}
}

// Helpers.

func compileBundle(t *testing.T, source string) *policy.Bundle {
	t.Helper()
	src, err := policy.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(src, "tenant_acme", "bundle_scenario", origin)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return bundle
}

func signBundleFor(t *testing.T, b *policy.Bundle) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if err := b.Sign(priv, "release-engineer", origin); err != nil {
		t.Fatalf("sign: %v", err)
	}
}

// activeBundle walks a bundle all the way to production, which is the only way to
// get one that enforces.
func activeBundle(t *testing.T, source string) *policy.Bundle {
	t.Helper()
	b := compileBundle(t, source)
	signBundleFor(t, b)
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := b.Transition(stage, origin, "release-engineer"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	if !b.Enforcing() {
		t.Fatal("the bundle is not enforcing after the full pipeline")
	}
	return b
}

// A guard against this file drifting away from the scenario definitions on disk.
func TestEveryScenarioDirectoryIsAccountedFor(t *testing.T) {
	entries, err := os.ReadDir("../../scenarios")
	if err != nil {
		t.Fatalf("read scenarios: %v", err)
	}

	implemented := map[string]bool{}
	for _, file := range []string{"fragmentation_test.go", "accumulation_test.go",
		"fleet_scenarios_test.go", "platform_scenarios_test.go"} {
		raw, readErr := os.ReadFile(file)
		if readErr != nil {
			t.Fatalf("read %s: %v", file, readErr)
		}
		body := string(raw)
		for i := 1; i <= 12; i++ {
			id := "S0" + itoa(i)
			if i >= 10 {
				id = "S" + itoa(i)
			}
			if strings.Contains(body, "func Test"+id+"_") {
				implemented[id] = true
			}
		}
	}

	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "S") {
			continue
		}
		id := strings.SplitN(entry.Name(), "_", 2)[0]
		if !implemented[id] {
			t.Errorf("scenario directory %s has no test; the stress library is "+
				"incomplete (spec section 41 requires all twelve)", entry.Name())
		}
	}

	if len(implemented) != 12 {
		t.Errorf("%d of 12 scenarios are implemented", len(implemented))
	}
}

// emptyPool is a trust bundle that trusts nothing, which is what the compromised
// workload is presenting its certificate against.
func emptyPool() *x509.CertPool { return x509.NewCertPool() }
