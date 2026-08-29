package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
)

// Spec section 12.2: an invalid signature denies. Nothing verified one — the envelope
// carried a signature field that no code read, and `agent_id` was a claim in the body
// while the authority grant is scoped to exactly that agent.
//
// These are the cases that make the binding real rather than decorative.

func otherKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

func submitSigned(t *testing.T, p *Pipeline, raw []byte) Result {
	t.Helper()
	return p.Submit(context.Background(), raw, presentedAPI())
}

func TestASignedEnvelopeProceeds(t *testing.T) {
	p, _, _ := harness(t)
	if result := submitSigned(t, p, envelope(nil)); !result.Accepted {
		t.Fatalf("a correctly signed intent was refused at %s: %s", result.Stage, result.Reason)
	}
}

func TestAnUnsignedEnvelopeIsRefused(t *testing.T) {
	p, _, _ := harness(t)

	unsigned := envelope(nil)
	var m map[string]any
	if err := json.Unmarshal(unsigned, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(m, "signature")
	stripped, _ := json.Marshal(m)

	// Refused at validation rather than at the signature stage, because the contract
	// requires the member and validation runs first. Both deny; what matters is that
	// an unsigned executable intent never reaches a decision, and that the refusal
	// says which field is missing.
	result := submitSigned(t, p, stripped)
	if result.Accepted {
		t.Fatal("an unsigned envelope was accepted")
	}
	if !strings.Contains(strings.Join(result.Details, " ")+result.Reason, "SIGNATURE") {
		t.Errorf("the refusal does not name the signature: %s %s / %v",
			result.Stage, result.Code, result.Details)
	}
}

// The binding that matters: a key registered to one agent must never verify an
// envelope claiming another. Without it an authenticated caller could submit under any
// agent id it liked, against grants scoped to exactly one.
func TestAKeyRegisteredToAnotherAgentDoesNotVerify(t *testing.T) {
	p, _, _ := harness(t)

	pub, priv := otherKey(t)
	keys := identity.NewMemoryKeys()
	keys.Add(identity.AgentKey{
		TenantID: "tenant_test", AgentID: "agent_somebody_else", KeyID: "key_other",
		Algorithm: identity.AlgorithmEd25519, PublicKey: pub,
		Status: "ACTIVE", ValidFrom: at.Add(-time.Hour),
	})
	p.Keys = keys

	raw := envelope(nil)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	delete(m, "signature")
	stripped, _ := json.Marshal(m)
	signed := sign(stripped, priv, "key_other")

	result := submitSigned(t, p, signed)
	if result.Accepted || result.Code != "SIGNATURE_KEY_UNKNOWN" {
		t.Errorf("accepted=%v code=%s, want SIGNATURE_KEY_UNKNOWN: a key belonging to "+
			"another agent verified this envelope", result.Accepted, result.Code)
	}
}

func TestARevokedKeyDoesNotVerify(t *testing.T) {
	p, _, _ := harness(t)

	revoked := at.Add(-time.Minute)
	keys := identity.NewMemoryKeys()
	keys.Add(identity.AgentKey{
		TenantID: "tenant_test", AgentID: "agent_test", KeyID: "key_test",
		Algorithm: identity.AlgorithmEd25519, PublicKey: testPub,
		Status: "REVOKED", ValidFrom: at.Add(-time.Hour), RevokedAt: &revoked,
	})
	p.Keys = keys

	result := submitSigned(t, p, envelope(nil))
	if result.Accepted || result.Code != "SIGNATURE_KEY_REVOKED" {
		t.Errorf("accepted=%v code=%s, want SIGNATURE_KEY_REVOKED", result.Accepted, result.Code)
	}
}

func TestAnExpiredKeyDoesNotVerify(t *testing.T) {
	p, _, _ := harness(t)

	keys := identity.NewMemoryKeys()
	keys.Add(identity.AgentKey{
		TenantID: "tenant_test", AgentID: "agent_test", KeyID: "key_test",
		Algorithm: identity.AlgorithmEd25519, PublicKey: testPub,
		Status: "ACTIVE", ValidFrom: at.Add(-2 * time.Hour), ValidUntil: at.Add(-time.Hour),
	})
	p.Keys = keys

	result := submitSigned(t, p, envelope(nil))
	if result.Accepted || result.Code != "SIGNATURE_KEY_EXPIRED" {
		t.Errorf("accepted=%v code=%s, want SIGNATURE_KEY_EXPIRED", result.Accepted, result.Code)
	}
}

// An algorithm the caller names is a downgrade attack unless the platform decides
// which values are acceptable.
func TestAnUnsupportedAlgorithmIsRefused(t *testing.T) {
	p, _, _ := harness(t)

	raw := envelope(nil)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	m["signature"].(map[string]any)["algorithm"] = "none"
	tampered, _ := json.Marshal(m)

	result := submitSigned(t, p, tampered)
	if result.Accepted || result.Code != "SIGNATURE_ALGORITHM_UNSUPPORTED" {
		t.Errorf("accepted=%v code=%s, want SIGNATURE_ALGORITHM_UNSUPPORTED",
			result.Accepted, result.Code)
	}
}

// Every field a signature is worth having covers. Each of these is a change an
// attacker would want to make between signing and submission.
func TestModifyingAnyFieldAfterSigningIsRefused(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"the notional": func(m map[string]any) {
			m["intent"].(map[string]any)["quantity"] = 999.0
		},
		"the instrument": func(m map[string]any) {
			m["intent"].(map[string]any)["instrument_id"] = "instr_us_equity_PENNY"
		},
		"the authority grant": func(m map[string]any) {
			m["authority_grant_id"] = "grant_somebody_elses"
		},
		"the idempotency key": func(m map[string]any) {
			m["idempotency_key"] = "idem-different"
		},
		"the agent": func(m map[string]any) {
			m["agent"].(map[string]any)["agent_id"] = "agent_somebody_else"
		},
		"the tenant": func(m map[string]any) {
			m["tenant_id"] = "tenant_victim"
		},
	}

	for what, tamper := range cases {
		t.Run(what, func(t *testing.T) {
			p, fake, _ := harness(t)

			raw := envelope(nil)
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			tamper(m)
			tampered, _ := json.Marshal(m)

			result := submitSigned(t, p, tampered)
			if result.Accepted {
				t.Fatalf("changing %s after signing was accepted", what)
			}
			// Tenant and agent changes are caught before the signature is even
			// checked, by the checks that own those questions. What must never
			// happen is the order reaching a venue.
			if fake.Submissions("coid-idem-01J8Z3K9QW") != 0 {
				t.Errorf("changing %s put an order at the venue", what)
			}
		})
	}
}

// The same envelope verifies the same way every time. A canonical form that depended
// on map iteration order would pass a test suite and fail in production.
func TestVerificationIsDeterministic(t *testing.T) {
	p, _, _ := harness(t)
	raw := envelope(nil)

	for i := 0; i < 20; i++ {
		env, err := intent.Decode(raw)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if err := identity.VerifyEnvelopeSignature(context.Background(), testKeys(), raw,
			env, at); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	_ = p
}

// Every event names the one before it.
//
// evidence.Event has carried CausationID since Phase 6 and the real producer never set
// it: an integration test built a chain by hand and passed, so the field was supported
// by everything except the thing that emits events.
func TestTheChainOfEventsNamesItsPredecessor(t *testing.T) {
	p, _, ev := harness(t)

	if result := p.Submit(context.Background(), envelope(nil), presentedAPI()); !result.Accepted {
		t.Fatalf("refused at %s: %s", result.Stage, result.Reason)
	}

	events := ev.all()
	if len(events) < 3 {
		t.Fatalf("%d events recorded; a submission produces a chain", len(events))
	}
	if events[0].CausationID != "" {
		t.Errorf("the first event names a predecessor %q; nothing came before it",
			events[0].CausationID)
	}
	for i := 1; i < len(events); i++ {
		if events[i].CausationID != events[i-1].EventID {
			t.Errorf("event %d (%s) is caused by %q, want %q (%s)",
				i, events[i].EventName, events[i].CausationID,
				events[i-1].EventID, events[i-1].EventName)
		}
	}
}

// A decision that cannot be recorded is not acted on.
//
// Evidence used to be written after the fact and its failure ignored, with a comment
// calling it telemetry. An order at a venue that the platform has no record of deciding
// is the state an assurance layer exists to make impossible — so the receipt is
// committed first, and if it cannot be, nothing is sent.
func TestAnUnrecordableDecisionNeverReachesTheVenue(t *testing.T) {
	p, fake, _ := harness(t)
	p.Evidence = failingSink{}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if result.Accepted {
		t.Fatal("an intent was accepted while its decision could not be recorded")
	}
	if result.Code != "EVIDENCE_UNAVAILABLE" {
		t.Errorf("code = %s, want EVIDENCE_UNAVAILABLE", result.Code)
	}
	if fake.Submissions("coid-idem-01J8Z3K9QW") != 0 {
		t.Error("the order reached the venue with no durable record of the decision")
	}
}
