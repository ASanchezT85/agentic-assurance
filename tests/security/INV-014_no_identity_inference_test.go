package security

import (
	"reflect"
	"strings"
	"testing"

	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
)

// INV-014: model identity must never be inferred from workload identity without
// evidence.
//
// SPIFFE proves which workload connected. It says nothing about which model produced
// the reasoning inside it. The commercially tempting bug is to let a strong workload
// attestation make the model claims look strong too, because "A2 attested, running
// model-y" reads better than "A2 attested, model DECLARED". The second one is the
// true statement.

func TestAttestationDoesNotTouchModelClaims(t *testing.T) {
	claims := intent.RuntimeClaims{
		ModelProvider: intent.Claim{Value: "provider-x", Verification: intent.VerificationDeclared},
		ModelFamily:   intent.Claim{Value: "model-y", Verification: intent.VerificationDeclared},
		ModelVersion:  intent.Claim{Value: "", Verification: intent.VerificationUnknown},
	}

	// The strongest identity V0 can establish.
	established := identity.Attested{
		Level:    intent.AttestationA2,
		SpiffeID: identity.SPIFFEID{TrustDomain: "acme.example", Path: "/ns/agents/sa/momentum"},
		Method:   "SPIFFE_X509_SVID",
	}

	got := identity.ModelClaims(established, claims)

	if !reflect.DeepEqual(got, claims) {
		t.Fatalf("identity verification altered the model claims (INV-014)\n got: %+v\nwant: %+v", got, claims)
	}
	if got.ModelFamily.Verification != intent.VerificationDeclared {
		t.Errorf("model_family became %s under A2; workload attestation is not model attestation",
			got.ModelFamily.Verification)
	}
	if got.ModelFamily.Verified() {
		t.Error("a DECLARED model claim reported itself verified under A2 (INV-014)")
	}
	if got.ModelVersion.Verification != intent.VerificationUnknown {
		t.Errorf("model_version was promoted from UNKNOWN to %s", got.ModelVersion.Verification)
	}
}

// The same, with nothing to overwrite.
//
// The test above passes claims that are already filled in, so an inference that only fills
// a *missing* claim slips past it — and filling in what the caller left empty is exactly
// what a well-meaning enrichment does. A mutation sweep put four lines in ModelClaims that
// set the provider from the SVID when the caller sent none, marked VERIFIED, and every
// suite stayed green.
func TestAnEmptyModelClaimIsNotFilledInFromTheWorkload(t *testing.T) {
	empty := intent.RuntimeClaims{}

	established := identity.Attested{
		Level:    intent.AttestationA2,
		SpiffeID: identity.SPIFFEID{TrustDomain: "acme.example", Path: "/ns/agents/sa/momentum"},
		Method:   "SPIFFE_X509_SVID",
	}

	got := identity.ModelClaims(established, empty)

	if !reflect.DeepEqual(got, empty) {
		t.Fatalf("a claim the caller did not make was filled in from the workload "+
			"identity (INV-014): %+v.\n\nA verified workload says which process "+
			"connected. Naming a model on its behalf turns an absence into a claim, and "+
			"marking that claim verified turns it into evidence nobody produced.", got)
	}
	for name, claim := range map[string]intent.Claim{
		"model_provider": got.ModelProvider,
		"model_family":   got.ModelFamily,
		"model_version":  got.ModelVersion,
	} {
		if claim.Value != "" {
			t.Errorf("%s came back as %q from an envelope that declared nothing", name, claim.Value)
		}
		if claim.Verified() {
			t.Errorf("%s reported itself verified with no evidence reference", name)
		}
	}
}

// The established identity carries no model field at all. An absence is hard to
// test, so this asserts the shape: if someone adds ModelFamily to Attested, this
// fails and points at the invariant before the field acquires callers.
func TestAttestedCarriesNoModelIdentity(t *testing.T) {
	rt := reflect.TypeOf(identity.Attested{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if strings.Contains(strings.ToLower(name), "model") {
			t.Errorf("identity.Attested has field %q; workload identity must not carry model identity (INV-014, ADR-006)", name)
		}
	}
}

// Resolve must never produce A3. A3 is provider-attested model identity, V0 has no
// mechanism for it, and deriving it from a workload certificate is precisely the
// inference this invariant forbids.
func TestResolveNeverProducesA3(t *testing.T) {
	v := emptyBundleVerifier()

	presentations := []identity.Presented{
		{},
		{APIIdentity: "app_1"},
		{SVID: selfSignedCert(t, "spiffe://acme.example/ns/agents/sa/momentum")},
		{SVID: selfSignedCert(t, "spiffe://acme.example/ns/agents/sa/momentum"), APIIdentity: "app_1"},
	}
	for i, p := range presentations {
		if got := v.Resolve(p); got.Level == intent.AttestationA3 {
			t.Errorf("presentation %d produced A3; V0 has no provider attestation (ADR-006)", i)
		}
	}
}

// The envelope-level half of the same rule, enforced in Phase 1 and still true here:
// an agent cannot write A3 into the envelope and have it stand.
func TestA3InAnEnvelopeIsStillRefused(t *testing.T) {
	e := baseEnvelope()
	e.Agent.Attestation = intent.Attestation{Level: intent.AttestationA3, Method: "TRUST_ME"}

	err := e.Validate()
	if err == nil {
		t.Fatal("A3 was accepted in an envelope (INV-014)")
	}
	if !err.(intent.ValidationErrors).Has("ATTESTATION_A3_NOT_SUPPORTED") {
		t.Errorf("wrong reason: %v", err.(intent.ValidationErrors).Codes())
	}
}

// A verified workload identity and a declared model are recorded as two separate
// facts with two separate strengths. Collapsing them into one confidence is the
// failure this invariant exists to prevent.
func TestWorkloadAndModelStrengthsStaySeparate(t *testing.T) {
	established := identity.Attested{
		Level:    intent.AttestationA2,
		SpiffeID: identity.SPIFFEID{TrustDomain: "acme.example", Path: "/ns/agents/sa/momentum"},
	}
	claims := intent.RuntimeClaims{
		ModelFamily: intent.Claim{Value: "model-y", Verification: intent.VerificationDeclared},
	}

	if established.Level != intent.AttestationA2 {
		t.Fatal("precondition")
	}
	if identity.ModelClaims(established, claims).ModelFamily.Verification == intent.VerificationVerified {
		t.Error("model claim reached VERIFIED on the strength of the workload attestation (INV-014)")
	}
}
