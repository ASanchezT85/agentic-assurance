// Package security holds the executable form of the invariants in
// docs/threat-model/README.md. Each file guards one invariant and is owned by the
// phase that first makes it violable (ADR-024).
package security

import (
	"agentic-assurance/internal/money"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
)

// INV-008: unknown provenance can never be represented as verified provenance.
//
// The failure this guards against is quiet and cheap to introduce: a decoder that
// defaults an absent verification level to DECLARED, or a claim that says VERIFIED
// about itself. Both would make a fleet concentration number look far better
// sourced than it is (spec section 25, P-004).

func baseEnvelope() intent.AgentExecutionEnvelope {
	return intent.AgentExecutionEnvelope{
		SchemaVersion:    intent.SchemaVersion,
		EnvelopeID:       "env_1",
		IdempotencyKey:   "idem_1",
		ReceivedAt:       time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC),
		TenantID:         "tenant_a",
		AuthorityGrantID: "grant_1",
		Principal:        intent.Principal{PrincipalID: "principal_1", AccountID: "account_1"},
		// Structurally signed: these tests are about provenance of model claims, and
		// an executable envelope carries a signature now.
		Signature: intent.Signature{Algorithm: "Ed25519", KeyID: "agent-key-01", Value: "aa"},
		Agent: intent.Agent{
			AgentID:     "agent_1",
			Attestation: intent.Attestation{Level: intent.AttestationA1},
		},
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     ptr(4200.0),
			TimeInForce:  intent.TIFDay,
		},
	}
}

// f and q build the exact financial types a decoded envelope carries. Tests may start
// from a float literal for readability; the platform never does.
func ptr(v float64) *money.Amount {
	a, err := money.FromFloat(v)
	if err != nil {
		panic(err)
	}
	return &a
}

func qty(v float64) *money.Quantity {
	x, err := money.QuantityFromFloat(v)
	if err != nil {
		panic(err)
	}
	return &x
}

// An absent verification level must resolve to UNKNOWN. Not DECLARED, not empty.
func TestAbsentVerificationBecomesUnknown(t *testing.T) {
	e := baseEnvelope()
	e.RuntimeClaims = intent.RuntimeClaims{
		ModelProvider: intent.Claim{Value: ""},
		ModelFamily:   intent.Claim{Value: ""},
		ModelVersion:  intent.Claim{Value: ""},
	}

	if err := e.Validate(); err != nil {
		t.Fatalf("unknown provenance is permitted (spec 12.3), got: %v", err)
	}
	for name, got := range map[string]intent.VerificationLevel{
		"model_provider": e.RuntimeClaims.ModelProvider.Verification,
		"model_family":   e.RuntimeClaims.ModelFamily.Verification,
		"model_version":  e.RuntimeClaims.ModelVersion.Verification,
	} {
		if got != intent.VerificationUnknown {
			t.Errorf("%s resolved to %q; an absent level must be UNKNOWN (INV-008)", name, got)
		}
	}
}

// A claim cannot verify itself. VERIFIED without an evidence reference is refused.
func TestVerifiedRequiresEvidence(t *testing.T) {
	e := baseEnvelope()
	e.RuntimeClaims.ModelFamily = intent.Claim{
		Value:        "model-y",
		Verification: intent.VerificationVerified,
	}

	err := e.Validate()
	if err == nil {
		t.Fatal("a self-declared VERIFIED claim was accepted (INV-008)")
	}
	if !err.(intent.ValidationErrors).Has("VERIFIED_WITHOUT_EVIDENCE") {
		t.Errorf("wrong reason: %v", err.(intent.ValidationErrors).Codes())
	}
}

// Claim.Verified is the single place that answers "may this be treated as
// verified". It must never say yes without evidence, whatever the level says.
func TestClaimVerifiedNeedsEvidence(t *testing.T) {
	cases := []struct {
		name string
		c    intent.Claim
		want bool
	}{
		{"verified with evidence", intent.Claim{Value: "m", Verification: intent.VerificationVerified, EvidenceRef: "ev_1"}, true},
		{"verified without evidence", intent.Claim{Value: "m", Verification: intent.VerificationVerified}, false},
		{"declared with evidence", intent.Claim{Value: "m", Verification: intent.VerificationDeclared, EvidenceRef: "ev_1"}, false},
		{"unknown", intent.Claim{}, false},
	}
	for _, tc := range cases {
		if got := tc.c.Verified(); got != tc.want {
			t.Errorf("%s: Verified() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An empty value cannot carry a level above UNKNOWN: declaring nothing is not a
// declaration.
func TestEmptyClaimCannotBeDeclared(t *testing.T) {
	e := baseEnvelope()
	e.RuntimeClaims.ModelProvider = intent.Claim{Value: "", Verification: intent.VerificationDeclared}

	err := e.Validate()
	if err == nil {
		t.Fatal("an empty DECLARED claim was accepted (INV-008)")
	}
	if !err.(intent.ValidationErrors).Has("CLAIM_WITHOUT_VALUE") {
		t.Errorf("wrong reason: %v", err.(intent.ValidationErrors).Codes())
	}
}

// INV-014 lives in Phase 2, but its Phase 1 half is enforceable now: an attested
// workload does not thereby have an attested model. A2 is about the workload, and
// claiming A3 without provider attestation is refused outright (ADR-006).
func TestWorkloadAttestationDoesNotVerifyTheModel(t *testing.T) {
	e := baseEnvelope()
	e.Agent.Attestation = intent.Attestation{Level: intent.AttestationA2, Method: "SPIFFE_X509_SVID"}
	e.Agent.WorkloadIdentity = intent.WorkloadIdentity{SpiffeID: "spiffe://acme.example/ns/a/sa/b"}
	e.RuntimeClaims.ModelFamily = intent.Claim{Value: "model-y", Verification: intent.VerificationDeclared}

	if err := e.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if e.RuntimeClaims.ModelFamily.Verification != intent.VerificationDeclared {
		t.Errorf("A2 attestation changed the model claim to %q (ADR-006, INV-014)",
			e.RuntimeClaims.ModelFamily.Verification)
	}
	if e.RuntimeClaims.ModelFamily.Verified() {
		t.Error("a DECLARED model claim reported itself as verified under A2 (INV-014)")
	}
}

func TestA3CannotBeClaimedInV0(t *testing.T) {
	e := baseEnvelope()
	e.Agent.Attestation = intent.Attestation{Level: intent.AttestationA3, Method: "VENDOR_SAYS_SO"}

	err := e.Validate()
	if err == nil {
		t.Fatal("A3 was accepted; V0 has no provider attestation (spec 11, ADR-006)")
	}
	if !err.(intent.ValidationErrors).Has("ATTESTATION_A3_NOT_SUPPORTED") {
		t.Errorf("wrong reason: %v", err.(intent.ValidationErrors).Codes())
	}
}
