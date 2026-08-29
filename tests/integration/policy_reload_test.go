//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/policy"
)

// Activation and rollback have to take effect on a running gateway.
//
// The provider read a tenant's signed bundle once and cached it forever, so replacing
// the active file — or rolling back to the previous one in the middle of an incident —
// did nothing until somebody restarted the process. Staged activation is an operational
// act, and one that needs a restart is not one.

func writeBundle(t *testing.T, dir, tenant, id string, priv ed25519.PrivateKey,
	now time.Time, source string) {

	t.Helper()

	parsed, err := policy.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(parsed, tenant, id, now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := bundle.Sign(priv, "reload-test", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, now, "reload-test"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, tenant+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Modification times have coarse resolution on some filesystems; move it forward
	// explicitly so the change is visible rather than depending on the clock.
	stamp := now.Add(time.Duration(len(id)) * time.Second)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

const denyEverything = `
version: 1
policy: pol_reload
rules:
  - id: stop_everything
    action: DENY
    when:
      notional_gt: 0
`

const allowOrdinaryOrders = `
version: 1
policy: pol_reload
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 1000000
`

func TestAReplacedBundleTakesEffectWithoutRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	tenant := "tenant_reload"

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	writeBundle(t, dir, tenant, "bundle_v1", priv, now, allowOrdinaryOrders)
	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}

	first, err := bundles.Active(context.Background(), tenant)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if first.BundleID != "bundle_v1" {
		t.Fatalf("bundle = %s, want bundle_v1", first.BundleID)
	}

	// The customer activates a stricter bundle. No restart.
	writeBundle(t, dir, tenant, "bundle_v2", priv, now, denyEverything)

	second, err := bundles.Active(context.Background(), tenant)
	if err != nil {
		t.Fatalf("active after replace: %v", err)
	}
	if second.BundleID != "bundle_v2" {
		t.Errorf("bundle = %s, want bundle_v2: the running gateway is still enforcing "+
			"the bundle it read at startup", second.BundleID)
	}

	// And rolling back is the same act in reverse, which is the half that matters
	// during an incident.
	writeBundle(t, dir, tenant, "bundle_v1", priv, now, allowOrdinaryOrders)
	rolledBack, err := bundles.Active(context.Background(), tenant)
	if err != nil {
		t.Fatalf("active after rollback: %v", err)
	}
	if rolledBack.BundleID != "bundle_v1" {
		t.Errorf("bundle = %s, want bundle_v1 after rollback", rolledBack.BundleID)
	}
}

// A candidate that does not verify must not become a partial activation.
func TestAnUnverifiableReplacementLeavesTheActiveBundleInForce(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	tenant := "tenant_reload_bad"

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	writeBundle(t, dir, tenant, "bundle_good", priv, now, allowOrdinaryOrders)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	if _, err := bundles.Active(context.Background(), tenant); err != nil {
		t.Fatalf("active: %v", err)
	}

	// Signed by somebody else entirely.
	_, attacker, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	writeBundle(t, dir, tenant, "bundle_forged", attacker, now, denyEverything)

	active, err := bundles.Active(context.Background(), tenant)
	if err != nil {
		t.Fatalf("a failed reload broke enforcement entirely: %v", err)
	}
	if active.BundleID != "bundle_good" {
		t.Errorf("bundle = %s, want bundle_good: an unsigned candidate was activated",
			active.BundleID)
	}
}

// A file that disappears is not an absence of policy.
func TestADeletedBundleKeepsTheOneInForce(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	tenant := "tenant_reload_gone"

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	writeBundle(t, dir, tenant, "bundle_only", priv, now, allowOrdinaryOrders)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	if _, err := bundles.Active(context.Background(), tenant); err != nil {
		t.Fatalf("active: %v", err)
	}

	if err := os.Remove(filepath.Join(dir, tenant+".json")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	active, err := bundles.Active(context.Background(), tenant)
	if err != nil || active == nil {
		t.Fatalf("a missing file dropped enforcement: %v", err)
	}
	if active.BundleID != "bundle_only" {
		t.Errorf("bundle = %s, want bundle_only", active.BundleID)
	}
}

// Enforcement changing is a fact about a customer's platform, so it is recorded.
//
// Reload took effect and left nothing behind. An incident review could see every
// decision a bundle produced and not which bundle was in force when, who activated it,
// or that a rollback had happened at all — while section 32 names
// policy.bundle.activated.v1 and rolled_back.v1 and nothing produced either.
func TestActivationAndRollbackAreRecorded(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_activation_%d", now.UnixNano())

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	store := evidence.NewStore(idemPool(t))
	writeBundle(t, dir, tenant, "bundle_a", priv, now, allowOrdinaryOrders)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	bundles.Evidence = store

	if _, err := bundles.Active(ctx, tenant); err != nil {
		t.Fatalf("active: %v", err)
	}
	// A second read of an unchanged file is not an activation: the operators did
	// nothing, and an event for it is noise in the customer's evidence.
	if _, err := bundles.Active(ctx, tenant); err != nil {
		t.Fatalf("active again: %v", err)
	}

	writeBundle(t, dir, tenant, "bundle_b", priv, now, denyEverything)
	if _, err := bundles.Active(ctx, tenant); err != nil {
		t.Fatalf("active after replace: %v", err)
	}

	// The retreat: the bundle this gateway was already running goes back in force.
	writeBundle(t, dir, tenant, "bundle_a", priv, now, allowOrdinaryOrders)
	if _, err := bundles.Active(ctx, tenant); err != nil {
		t.Fatalf("active after rollback: %v", err)
	}

	events, err := store.ByAggregate(ctx, tenant, "bundle_a")
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	b, err := store.ByAggregate(ctx, tenant, "bundle_b")
	if err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	events = append(events, b...)

	var activated, rolledBack int
	for _, e := range events {
		switch e.EventName {
		case evidence.PolicyBundleActivated:
			activated++
			if e.Payload["activated_by"] != "reload-test" {
				t.Errorf("the activation names %v as its actor; a change to what the "+
					"platform denies without a name attached is unattributable",
					e.Payload["activated_by"])
			}
			if e.Payload["content_hash"] == nil || e.Payload["content_hash"] == "" {
				t.Error("the activation does not say which content took effect")
			}
		case evidence.PolicyBundleRolledBack:
			rolledBack++
		}
	}

	// Two activations, bundle_a then bundle_b, and one rollback. Not three
	// activations: an unchanged file was read twice and a bundle already in force
	// came back, and a review that reads a retreat as a release is reading the
	// incident backwards.
	if activated != 2 {
		t.Errorf("activated = %d, want 2; got %d events", activated, len(events))
	}
	if rolledBack != 1 {
		t.Errorf("rolled back = %d, want 1; restoring the previous bundle during an "+
			"incident is not the same act as shipping a new one", rolledBack)
	}
}
