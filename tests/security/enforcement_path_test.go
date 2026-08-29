package security

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// The invariants, asserted through the composition root.
//
// Every other file here proves one invariant against its own package: identity
// against internal/identity, the authority ceiling against internal/authority, the
// policy engine against internal/policy. Each of those is correct and none of them
// reaches the place where the invariants have to hold together.
//
// That gap is not hypothetical. Ripping CheckClaim, RequireTenant and
// RequireExecutable out of internal/gateway leaves this entire suite green: every
// primitive still behaves, and nothing calls them. INV-007 was exploitable for exactly
// that reason — the database half was enforced and tested from the start, the
// transport half was never written, and a guard on one half of a boundary reads as a
// guard on the boundary.
//
// So: a real pipeline, real engines, and the invariants asserted against what a
// request actually does. TestPathEnforcedInvariantsAreCoveredHere keeps the list
// honest.

var pathAt = time.Date(2026, 8, 28, 15, 0, 0, 0, time.UTC)

type pathRig struct {
	pipeline *gateway.Pipeline
	broker   *fakebroker.Broker
	usage    *authority.MemoryUsage
	grants   pathGrants
}

type pathGrants map[string]*authority.Grant

func (g pathGrants) Load(_ context.Context, _, grantID string) (*authority.Grant, error) {
	grant, ok := g[grantID]
	if !ok {
		return nil, errNoGrant{}
	}
	return grant, nil
}

type errNoGrant struct{}

func (errNoGrant) Error() string { return "no such grant" }

type pathBundles struct{ bundle *policy.Bundle }

func (b pathBundles) Active(context.Context, string) (*policy.Bundle, error) {
	return b.bundle, nil
}

func newPathRig(t *testing.T) *pathRig {
	t.Helper()

	venue := fakebroker.New()
	venue.SetClock(func() time.Time { return pathAt })

	grants := pathGrants{"grant_path": pathGrant()}
	usage := authority.NewMemoryUsage()

	// The signing key this rig's envelopes are signed with. Registered rather than
	// bypassed: a path test that skipped signature verification would be testing a
	// pipeline nobody runs.
	keys := identity.NewMemoryKeys()
	keys.Add(identity.AgentKey{
		TenantID: "tenant_path", AgentID: "agent_path", KeyID: "key_path",
		Algorithm: identity.AlgorithmEd25519, PublicKey: pathPub,
		Status: "ACTIVE", ValidFrom: pathAt.Add(-time.Hour),
	})

	return &pathRig{
		broker: venue,
		usage:  usage,
		grants: grants,
		pipeline: &gateway.Pipeline{
			Identity: &identity.Verifier{},
			Grants:   grants,
			Policies: pathBundles{pathBundle(t)},
			Usage:    usage,
			Reserve:  usage,
			Keys:     keys,
			Execution: &execution.Service{
				Broker: venue,
				Store:  execution.NewMemoryStore(),
				Now:    func() time.Time { return pathAt },
			},
			Symbols: gateway.StaticSymbols{"instr_us_equity_00206R102": "AAPL"},
			Now:     func() time.Time { return pathAt },
		},
	}
}

func pathGrant() *authority.Grant {
	return &authority.Grant{
		GrantID:             "grant_path",
		TenantID:            "tenant_path",
		PrincipalID:         "prin_path",
		AccountID:           "acct_path",
		AgentID:             "agent_path",
		IssuedAt:            pathAt.Add(-time.Hour),
		ValidFrom:           pathAt.Add(-time.Hour),
		ValidUntil:          pathAt.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102"},
		Limits:              authority.Limits{PerOrderNotional: 5000},
		Status:              authority.StatusActive,
	}
}

func pathBundle(t *testing.T) *policy.Bundle {
	t.Helper()
	src, err := policy.ParseSource([]byte(`
version: 1
policy: pol_path
rules:
  - id: no_extended_hours
    action: DENY
    when:
      extended_hours: true
`))
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	bundle, err := policy.Compile(src, "tenant_path", "bundle_path", pathAt)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if err := bundle.Sign(priv, "path", pathAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, to := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(to, pathAt, "path"); err != nil {
			t.Fatalf("transition %s: %v", to, err)
		}
	}
	return bundle
}

// pathEnvelope is a valid, authorized intent. Mutations make it something else.
func pathEnvelope(mutate func(map[string]any)) []byte {
	m := map[string]any{
		"schema_version":  "0.1",
		"envelope_id":     "env_path",
		"idempotency_key": "idem-path",
		"correlation_id":  "corr_path",
		"received_at":     pathAt.Format(time.RFC3339),
		"tenant_id":       "tenant_path",
		"principal":       map[string]any{"principal_id": "prin_path", "account_id": "acct_path"},
		"agent": map[string]any{
			"agent_id":    "agent_path",
			"attestation": map[string]any{"level": "A1", "method": "api_key"},
		},
		"authority_grant_id": "grant_path",
		"intent": map[string]any{
			"instrument_id": "instr_us_equity_00206R102",
			"asset_class":   "EQUITY",
			"side":          "BUY",
			"order_type":    "LIMIT",
			"quantity":      10,
			"limit_price":   100,
			"time_in_force": "DAY",
		},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, _ := json.Marshal(m)

	value, err := identity.SignEnvelope(raw, pathPriv)
	if err != nil {
		panic(err)
	}
	m["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519, "key_id": "key_path", "value": value,
	}
	signed, _ := json.Marshal(m)
	return signed
}

// One key for this file's envelopes.
var pathPub, pathPriv = func() (ed25519.PublicKey, ed25519.PrivateKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return pub, priv
}()

func pathCaller() identity.Presented {
	return identity.Presented{APIIdentity: "svc_path", TenantID: "tenant_path"}
}

func (r *pathRig) submit(raw []byte, who identity.Presented) gateway.Result {
	return r.pipeline.Submit(context.Background(), raw, who)
}

// The baseline. If this fails everything below is measuring the wrong thing: a
// pipeline that refuses everything satisfies every denial assertion trivially.
func TestPathAcceptsAValidAuthorizedIntent(t *testing.T) {
	rig := newPathRig(t)

	result := rig.submit(pathEnvelope(nil), pathCaller())
	if !result.Accepted {
		t.Fatalf("a valid authorized intent was refused at %s: %s (%s)",
			result.Stage, result.Code, result.Reason)
	}
	if rig.broker.Submissions("coid-idem-path") != 1 {
		t.Fatal("nothing reached the venue")
	}
}

// INV-001 on the path. An unauthenticated workload cannot produce an executable order,
// no matter what the envelope says about itself.
func TestPathRefusesAnUnauthenticatedWorkload(t *testing.T) {
	rig := newPathRig(t)

	result := rig.submit(pathEnvelope(nil), identity.Presented{})

	if result.Accepted {
		t.Fatal("an unauthenticated caller produced an executable order (INV-001)")
	}
	if result.Stage != "IDENTITY" {
		t.Errorf("stage = %s, want IDENTITY: %s", result.Stage, result.Reason)
	}
	if rig.broker.Submissions("coid-idem-path") != 0 {
		t.Error("the order reached the venue anyway")
	}
}

// INV-008 on the path. An envelope cannot claim more attestation than the transport
// established, and the claim does not raise what was established.
func TestPathRefusesAClaimAboveTheEvidence(t *testing.T) {
	rig := newPathRig(t)

	raw := pathEnvelope(func(m map[string]any) {
		agent := m["agent"].(map[string]any)
		agent["attestation"] = map[string]any{"level": "A2", "method": "spiffe"}
		agent["workload_identity"] = map[string]any{
			"spiffe_id": "spiffe://tenant.example/ns/prod/sa/agent",
		}
	})

	result := rig.submit(raw, pathCaller())
	if result.Accepted {
		t.Fatal("an envelope claiming A2 over an A1 transport was accepted (INV-008)")
	}
	if result.Attested.Level != "A1" {
		t.Errorf("established level = %s; a claim must not raise it", result.Attested.Level)
	}
}

// INV-007 on the path. A caller authenticated for one tenant cannot act on another.
func TestPathRefusesACrossTenantIntent(t *testing.T) {
	rig := newPathRig(t)

	victim := pathGrant()
	victim.TenantID = "tenant_victim"
	victim.GrantID = "grant_victim"
	rig.grants["grant_victim"] = victim

	raw := pathEnvelope(func(m map[string]any) {
		m["tenant_id"] = "tenant_victim"
		m["authority_grant_id"] = "grant_victim"
	})

	result := rig.submit(raw, pathCaller())
	if result.Accepted {
		t.Fatal("a caller authenticated for tenant_path placed an order for " +
			"tenant_victim (INV-007)")
	}
	if rig.broker.Submissions("coid-idem-path") != 0 {
		t.Error("the order reached the venue anyway")
	}
}

// INV-002 on the path. No intent exceeds its grant, and the ceiling is applied before
// anything reaches a venue rather than reported afterwards.
func TestPathEnforcesTheAuthorityCeiling(t *testing.T) {
	rig := newPathRig(t)

	// 100 x 100 = 10,000 against a 5,000 per-order limit.
	raw := pathEnvelope(func(m map[string]any) {
		m["intent"].(map[string]any)["quantity"] = 100
	})

	result := rig.submit(raw, pathCaller())
	if result.Accepted {
		t.Fatal("an order over its grant's ceiling was accepted (INV-002)")
	}
	if result.Code != "PER_ORDER_LIMIT_EXCEEDED" {
		t.Errorf("code = %s, want PER_ORDER_LIMIT_EXCEEDED", result.Code)
	}
	if rig.broker.Submissions("coid-idem-path") != 0 {
		t.Error("the order reached the venue before the ceiling was applied")
	}
}

// INV-003 on the path. The decision is a pure function of the envelope, the grant, the
// bundle and the clock: the same request decides the same way, and the bundle that
// decided travels with the decision.
func TestPathDecidesDeterministically(t *testing.T) {
	first := newPathRig(t)
	second := newPathRig(t)

	raw := pathEnvelope(func(m map[string]any) {
		m["intent"].(map[string]any)["extended_hours"] = true
	})

	a := first.submit(raw, pathCaller())
	b := second.submit(raw, pathCaller())

	if a.Accepted || b.Accepted {
		t.Fatal("a policy-denied intent was accepted")
	}
	if a.Code != b.Code || a.Stage != b.Stage {
		t.Errorf("two identical requests decided differently: %s/%s and %s/%s",
			a.Stage, a.Code, b.Stage, b.Code)
	}
	if a.Policy == nil || a.Policy.ContentHash == "" {
		t.Fatal("the decision does not carry the bundle that produced it")
	}
	if a.Policy.ContentHash != b.Policy.ContentHash {
		t.Error("the same bundle produced two content hashes")
	}
	if a.Policy.DecidedBy != "no_extended_hours" {
		t.Errorf("decided by %q, want the rule that matched", a.Policy.DecidedBy)
	}
}

// INV-004 on the path. A lost venue response is not a failure and is never resubmitted
// blindly.
func TestPathDoesNotRetryAnAmbiguousOutcome(t *testing.T) {
	rig := newPathRig(t)
	rig.broker.InjectFault("coid-idem-path", fakebroker.FaultTimeoutAfterReceipt)

	result := rig.submit(pathEnvelope(nil), pathCaller())

	if n := rig.broker.Submissions("coid-idem-path"); n != 1 {
		t.Errorf("the venue received %d submissions after a lost response, want 1 (INV-004)", n)
	}
	if result.Outcome != nil && result.Outcome.State == broker.StateRejected {
		t.Error("a lost response became a rejection; the venue never refused anything")
	}
}

// INV-011 on the path. The idempotency answer is authoritative without a cache: a
// duplicate returns the prior outcome when Redis is absent entirely, which is the
// only configuration this rig has.
func TestPathIsIdempotentWithoutACache(t *testing.T) {
	rig := newPathRig(t)

	if r := rig.submit(pathEnvelope(nil), pathCaller()); !r.Accepted {
		t.Fatalf("first submission refused: %s", r.Reason)
	}
	second := rig.submit(pathEnvelope(nil), pathCaller())

	if !second.Replayed {
		t.Error("a duplicate was not served from the record (INV-002, INV-011)")
	}
	if n := rig.broker.Submissions("coid-idem-path"); n != 1 {
		t.Errorf("the venue received %d submissions for one idempotency key, want 1", n)
	}
}

// INV-005 on the path. Losing telemetry degrades the audit trail and nothing else: a
// decision that failed because the evidence store was down would be the audit trail
// causing the incident.
func TestPathEnforcesWithoutTelemetry(t *testing.T) {
	rig := newPathRig(t)
	rig.pipeline.Evidence = brokenSink{}

	if r := rig.submit(pathEnvelope(nil), pathCaller()); !r.Accepted {
		t.Errorf("a decision failed because telemetry was unavailable: %s (INV-005)", r.Reason)
	}
}

type brokenSink struct{}

func (brokenSink) Append(context.Context, evidence.Event) (bool, error) {
	return false, errNoGrant{}
}

func (brokenSink) AppendBatch(context.Context, []evidence.Event) error {
	return errNoGrant{}
}

// INV-012 on the path. A venue that answers with something the core does not know does
// not become a plausible state; the platform reports it as unresolved.
func TestPathContainsAnAdapterFailure(t *testing.T) {
	rig := newPathRig(t)
	rig.broker.InjectFault("coid-idem-path", fakebroker.FaultReject)

	result := rig.submit(pathEnvelope(nil), pathCaller())

	if result.Accepted {
		t.Error("a venue rejection was reported as an accepted order")
	}
	if result.Outcome == nil {
		t.Fatal("a venue rejection produced no outcome at all")
	}
	if result.Outcome.State != broker.StateRejected {
		t.Errorf("state = %s, want REJECTED", result.Outcome.State)
	}
}

// INV-015 on the path. An envelope the platform cannot make sense of is refused before
// anything is evaluated, rather than normalised into something plausible.
func TestPathRefusesAnUnnormalisableIntent(t *testing.T) {
	rig := newPathRig(t)

	// A ticker where a canonical instrument identifier belongs.
	raw := pathEnvelope(func(m map[string]any) {
		m["intent"].(map[string]any)["instrument_id"] = "AAPL"
	})

	result := rig.submit(raw, pathCaller())
	if result.Accepted {
		t.Fatal("an intent with a ticker for an instrument id was accepted (INV-015)")
	}
	if rig.broker.Submissions("coid-idem-path") != 0 {
		t.Error("it reached the venue")
	}
}

// INV-001 on the path, the case the previous test cannot reach.
//
// An envelope claiming A1 over an A0 transport is refused by the claim check, so that
// test passes whether or not the executability check exists. An envelope that declares
// itself A0 honestly claims nothing it was not given, passes the claim check, and must
// still be refused: an unauthenticated workload cannot produce an executable order no
// matter how truthfully it says so.
//
// Written after removing RequireExecutable from the pipeline left this file green.
func TestPathRefusesAnHonestlyUnattestedIntent(t *testing.T) {
	rig := newPathRig(t)

	raw := pathEnvelope(func(m map[string]any) {
		m["agent"].(map[string]any)["attestation"] = map[string]any{
			"level": "A0", "method": "none",
		}
	})

	result := rig.submit(raw, identity.Presented{})

	if result.Accepted {
		t.Fatal("an intent that honestly declared itself unattested produced an " +
			"executable order (INV-001)")
	}
	// The code, not only the refusal. Several checks refuse an unauthenticated
	// caller, and a test that accepted any of them would pass with the executability
	// check removed — which is exactly what it did before this line existed.
	if result.Code != "UNAUTHENTICATED_WORKLOAD" {
		t.Errorf("code = %s, want UNAUTHENTICATED_WORKLOAD; something else refused it "+
			"and the executability check may not be running", result.Code)
	}
	if rig.broker.Submissions("coid-idem-path") != 0 {
		t.Error("the order reached the venue anyway")
	}
}

// The meta-guard: an invariant enforced on the request path needs a test that reaches
// the request path.
//
// This is the check that was missing, and its absence is why INV-007 was exploitable.
// The completeness gate asks whether every invariant has a file. Every one did. It
// never asked whether the file reaches the place the invariant is enforced, and each
// of them tested its own package in isolation: identity against internal/identity,
// the ceiling against internal/authority, the policy engine against internal/policy.
// A guard on one half of a boundary reads as a guard on the boundary.
//
// The list is a claim, and it is meant to be argued with. An invariant on it is one
// the composition root decides; an invariant off it is enforced somewhere else — by a
// database constraint, by a structural rule about imports, or by a package that no
// request passes through. Adding one is cheap. Removing one should require saying why.
func TestPathEnforcedInvariantsAreCoveredHere(t *testing.T) {
	pathEnforced := map[string]string{
		"INV-001": "an unauthenticated workload cannot produce an executable order",
		"INV-002": "no intent exceeds its authority grant",
		"INV-003": "the decision is a pure function of its inputs",
		"INV-004": "an ambiguous outcome is never blindly retried",
		"INV-005": "enforcement continues without telemetry",
		"INV-007": "a caller cannot act on another tenant",
		"INV-008": "a claim cannot exceed the evidence",
		"INV-011": "the idempotency answer is authoritative without a cache",
		"INV-012": "an adapter failure cannot corrupt the domain model",
		"INV-015": "an unnormalisable instrument is refused",
	}

	source, err := os.ReadFile("enforcement_path_test.go")
	if err != nil {
		t.Fatalf("read this file: %v", err)
	}
	text := string(source)

	if !strings.Contains(text, `"agentic-assurance/internal/gateway"`) {
		t.Fatal("this file does not import the composition root, so nothing here " +
			"reaches the path these invariants are enforced on")
	}

	for id, what := range pathEnforced {
		if !strings.Contains(text, id) {
			t.Errorf("%s is enforced on the request path (%s) and no test in this file "+
				"names it. Its own INV file proves the primitive; nothing proves the "+
				"path calls it.", id, what)
		}
	}
}
