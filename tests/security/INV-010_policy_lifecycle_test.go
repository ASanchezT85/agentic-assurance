package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"testing"

	"agentic-assurance/internal/policy"
)

// INV-010: a new policy cannot reach production without versioning and validation.
//
// Two ways this gets broken. The obvious one is a shortcut transition from DRAFT
// straight to ACTIVE. The subtler one is arriving at ACTIVE through the proper
// sequence while carrying no version, no hash or no signature, because the pipeline
// checked the order of the steps and not their results.

func signed(t *testing.T) (*policy.Bundle, ed25519.PublicKey) {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/policies/valid/retail_agent_standard.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src, err := policy.ParseSource(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b, err := policy.Compile(src, "tenant_acme", "bundle_1", policyAt)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if err := b.Sign(priv, "release-engineer", policyAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return b, pub
}

// There is no shortcut to production. Every stage the spec names has to happen.
func TestNoShortcutToProduction(t *testing.T) {
	b, _ := signed(t)

	// Straight from SIGN to ACTIVE, skipping simulate, shadow and canary.
	if err := b.Transition(policy.StatusActive, policyAt, "someone-in-a-hurry"); err == nil {
		t.Fatal("a bundle jumped from SIGN to ACTIVE (INV-010)")
	}
	if b.Enforcing() {
		t.Fatal("the bundle is enforcing after a refused transition")
	}

	for _, s := range []policy.Status{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive} {
		if err := b.Transition(s, policyAt, "release-engineer"); err != nil {
			t.Fatalf("the full pipeline should work: %s: %v", s, err)
		}
	}
	if !b.Enforcing() {
		t.Error("a bundle that walked the whole pipeline is not enforcing")
	}
}

// The pipeline order alone is not enough. A bundle reaching the gate without a
// version, a hash or a signature must be refused there.
func TestProductionRequiresVersionHashAndSignature(t *testing.T) {
	cases := []struct {
		name     string
		sabotage func(*policy.Bundle)
	}{
		{"no version", func(b *policy.Bundle) { b.Version = 0 }},
		{"no content hash", func(b *policy.Bundle) { b.ContentHash = "" }},
		{"no signature", func(b *policy.Bundle) { b.Signature = "" }},
		{"no rules", func(b *policy.Bundle) { b.Rules = nil }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := signed(t)
			for _, s := range []policy.Status{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary} {
				if err := b.Transition(s, policyAt, "release-engineer"); err != nil {
					t.Fatalf("staging: %v", err)
				}
			}

			tc.sabotage(b)

			if err := b.Transition(policy.StatusActive, policyAt, "release-engineer"); err == nil {
				t.Fatalf("a bundle with %s reached production (INV-010)", tc.name)
			}
			if b.Enforcing() {
				t.Errorf("a bundle with %s is enforcing", tc.name)
			}
		})
	}
}

// A policy that does not compile never becomes a bundle at all, so there is nothing
// to promote. Validation is not a stage that can be skipped; it is the only way to
// obtain the object the later stages operate on.
func TestUnvalidatedPolicyNeverBecomesABundle(t *testing.T) {
	for _, fixture := range []string{
		"../fixtures/policies/invalid/no_version.yaml",
		"../fixtures/policies/invalid/duplicate_ids.yaml",
		"../fixtures/policies/invalid/impossible_bounds.yaml",
		"../fixtures/policies/invalid/no_rules.yaml",
	} {
		raw, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatalf("read %s: %v", fixture, err)
		}
		src, err := policy.ParseSource(raw)
		if err != nil {
			continue // rejected even earlier
		}
		if _, err := policy.Compile(src, "tenant_acme", "bundle_1", policyAt); err == nil {
			t.Errorf("%s compiled into a promotable bundle (INV-010)", fixture)
		}
	}
}

// Backwards moves are refused. A bundle cannot be walked back to DRAFT and re-edited;
// production policy is never edited in place (spec section 43).
func TestLifecycleDoesNotRunBackwards(t *testing.T) {
	b, _ := signed(t)
	for _, s := range []policy.Status{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive} {
		if err := b.Transition(s, policyAt, "release-engineer"); err != nil {
			t.Fatalf("staging: %v", err)
		}
	}

	for _, s := range []policy.Status{policy.StatusDraft, policy.StatusValidated, policy.StatusCompiled,
		policy.StatusSigned, policy.StatusShadow, policy.StatusCanary} {
		if err := b.Transition(s, policyAt, "someone"); err == nil {
			t.Errorf("an ACTIVE bundle moved back to %s (spec section 43)", s)
		}
	}
}

// Rollback is available from every staged state and is always audited.
func TestRollbackIsAlwaysAvailableAndAudited(t *testing.T) {
	stages := [][]policy.Status{
		{},
		{policy.StatusSimulated},
		{policy.StatusSimulated, policy.StatusShadow},
		{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary},
		{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive},
	}

	for i, path := range stages {
		b, _ := signed(t)
		for _, s := range path {
			if err := b.Transition(s, policyAt, "release-engineer"); err != nil {
				t.Fatalf("case %d staging: %v", i, err)
			}
		}
		if err := b.Rollback(policyAt, "operator", "elevated denial rate"); err != nil {
			t.Fatalf("case %d rollback from %s: %v", i, b.Activation.Status, err)
		}
		if b.Enforcing() {
			t.Errorf("case %d: a rolled-back bundle is still enforcing", i)
		}
		if b.Activation.RolledBackBy == "" || b.Activation.RollbackReason == "" {
			t.Errorf("case %d: rollback was not audited", i)
		}
	}
}

// Promotion must not change what the bundle enforces. If activation altered the
// hash, no signature could survive reaching production.
func TestPromotionPreservesTheSignedContents(t *testing.T) {
	b, pub := signed(t)
	hashAtSigning := b.ContentHash

	for _, s := range []policy.Status{policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive} {
		if err := b.Transition(s, policyAt, "release-engineer"); err != nil {
			t.Fatalf("staging: %v", err)
		}
	}

	if b.ContentHash != hashAtSigning {
		t.Error("promotion changed the content hash (INV-010)")
	}
	if err := b.VerifySignature(pub); err != nil {
		t.Errorf("the bundle in production does not verify: %v", err)
	}
}
