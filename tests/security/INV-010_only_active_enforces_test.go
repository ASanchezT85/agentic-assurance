package security

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/policy"
)

// INV-010: a new policy cannot reach production without versioning and validation.
//
// A bundle moves SIMULATED -> SHADOW -> CANARY -> ACTIVE, and only the last of those is
// production enforcement. The gateway checks it: `if !bundle.Enforcing()` refuses to load
// anything else. A mutation sweep removed that check and every suite the gate runs stayed
// green — the invariant was named in four test files and asserted in none of them at the
// place it is enforced.
//
// What the mutation would have allowed: a bundle staged for shadow evaluation, which is by
// definition a policy nobody has approved for production, deciding what a customer's agents
// may do. The lifecycle exists precisely so that the answer to "who approved this rule" is
// never "whoever last wrote the file".

func TestOnlyAnActiveBundleEnforces(t *testing.T) {
	for _, status := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary,
	} {
		t.Run(string(status), func(t *testing.T) {
			dir := t.TempDir()
			tenant := "tenant_inv010"
			pub := stageBundle(t, dir, tenant, status)

			bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
			if err != nil {
				t.Fatalf("bundles: %v", err)
			}

			active, err := bundles.Active(context.Background(), tenant)
			if err == nil {
				t.Fatalf("a %s bundle was loaded as the policy in force (%s).\n\n"+
					"SHADOW and CANARY are evaluation stages: a rule that has not "+
					"reached ACTIVE is one nobody has approved for production, and "+
					"letting it decide what an agent may do makes the lifecycle "+
					"decorative (INV-010).", status, active.BundleID)
			}
			if !strings.Contains(err.Error(), string(status)) {
				t.Errorf("the refusal does not say what stage the bundle is in: %v", err)
			}
		})
	}
}

// And the one that does reach production is refused too when nothing authorized it, which
// is the other half: the stage says the rules were validated, the activation says the
// customer approved them.
func TestAnActiveBundleWithoutAnAuthorizationDoesNotEnforce(t *testing.T) {
	dir := t.TempDir()
	tenant := "tenant_inv010_active"
	pub := stageBundle(t, dir, tenant, policy.StatusActive)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}

	// No activation store, so nothing can attribute the change to a customer.
	if active, err := bundles.Active(context.Background(), tenant); err == nil {
		t.Fatalf("an ACTIVE bundle enforced with no accepted activation behind it (%s); "+
			"reaching the last stage of the lifecycle is not the same as a customer "+
			"having authorized it (INV-009, ADR-028)", active.BundleID)
	}
}

// stageBundle writes a signed bundle at a lifecycle stage and returns the public key.
func stageBundle(t *testing.T, dir, tenant string, status policy.Status) ed25519.PublicKey {
	t.Helper()
	now := time.Now().UTC()

	source, err := policy.ParseSource([]byte(`
version: 1
policy: pol_inv010
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 1000
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(source, tenant, "bundle_inv010", now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if err := bundle.Sign(priv, "inv010", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, now, "inv010"); err != nil {
			t.Fatalf("transition %s: %v", stage, err)
		}
		if stage == status {
			break
		}
	}

	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, tenant+".json"), raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return pub
}
