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

	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/policy"
)

// Activation and rollback have to take effect on a running gateway.
//
// The provider read a tenant's signed bundle once and cached it forever, so replacing
// the active file — or rolling back to the previous one in the middle of an incident —
// did nothing until somebody restarted the process. Staged activation is an operational
// act, and one that needs a restart is not one.

// reloadAuthority is the activation key these tests sign authorizations with, plus the
// store that holds the accepted transitions. Registered once per tenant.
type reloadAuthority struct {
	store *policy.ActivationStore
	priv  ed25519.PrivateKey
	keyID string
}

func newReloadAuthority(t *testing.T, tenant string) *reloadAuthority {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("activation keygen: %v", err)
	}
	// A distinct key id per authority. They shared one, and while RegisterKey upserted
	// that was invisible: a second authority for the same tenant replaced the first key,
	// and the last one created was the one that verified. Now a key is never overwritten,
	// so two authorities sharing an id would mean the second one's signatures are checked
	// against the first one's key.
	keyID := fmt.Sprintf("reload_key_%d", time.Now().UnixNano())
	store := policy.NewActivationStore(idemPool(t))
	if _, err := store.RegisterKey(context.Background(), policy.ActivationKey{
		TenantID: tenant, KeyID: keyID, PublicKey: pub,
		Holder: "ops@example.test", Status: "ACTIVE",
		ValidFrom: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("register activation key: %v", err)
	}
	return &reloadAuthority{store: store, priv: priv, keyID: keyID}
}

// writeBundle stages a signed bundle and, when an authority is supplied, the customer
// authorization that lets it enforce. A bundle without one is staged but not activated,
// which is the point of the second signature.
func writeBundle(t *testing.T, dir, tenant, id string, priv ed25519.PrivateKey,
	now time.Time, source string, authority *reloadAuthority) {

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
	stamp := time.Now().Add(time.Duration(len(id)+len(raw)) * time.Millisecond)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	if authority == nil {
		return
	}
	authorization := policy.Authorization{
		SchemaVersion:     policy.AuthorizationSchemaVersion,
		TenantID:          tenant,
		BundleID:          bundle.BundleID,
		BundleContentHash: bundle.ContentHash,
		Action:            policy.ActionActivate,
		Actor:             "reload-test",
		AuthorizedAt:      time.Now().UTC(),
		Nonce:             fmt.Sprintf("reload_%s_%d", id, time.Now().UnixNano()),
	}
	if err := authorization.Sign(authority.priv, authority.keyID); err != nil {
		t.Fatalf("sign authorization: %v", err)
	}
	authRaw, err := json.MarshalIndent(authorization, "", "  ")
	if err != nil {
		t.Fatalf("marshal authorization: %v", err)
	}
	authPath := filepath.Join(dir, tenant+".activation.json")
	if err := os.WriteFile(authPath, authRaw, 0o600); err != nil {
		t.Fatalf("write authorization: %v", err)
	}
	if err := os.Chtimes(authPath, stamp, stamp); err != nil {
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
	tenant := fmt.Sprintf("tenant_reload_%d", time.Now().UnixNano())

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	authority := newReloadAuthority(t, tenant)

	writeBundle(t, dir, tenant, "bundle_v1", priv, now, allowOrdinaryOrders, authority)
	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	bundles.Activations = authority.store

	first, err := bundles.Active(context.Background(), tenant)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if first.BundleID != "bundle_v1" {
		t.Fatalf("bundle = %s, want bundle_v1", first.BundleID)
	}

	// The customer activates a stricter bundle. No restart.
	writeBundle(t, dir, tenant, "bundle_v2", priv, now, denyEverything, authority)

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
	writeBundle(t, dir, tenant, "bundle_v1", priv, now, allowOrdinaryOrders, authority)
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
	tenant := fmt.Sprintf("tenant_reload_bad_%d", time.Now().UnixNano())

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	authority := newReloadAuthority(t, tenant)
	writeBundle(t, dir, tenant, "bundle_good", priv, now, allowOrdinaryOrders, authority)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	bundles.Activations = authority.store
	if _, err := bundles.Active(context.Background(), tenant); err != nil {
		t.Fatalf("active: %v", err)
	}

	// Signed by somebody else entirely.
	_, attacker, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	writeBundle(t, dir, tenant, "bundle_forged", attacker, now, denyEverything, authority)

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
	tenant := fmt.Sprintf("tenant_reload_gone_%d", time.Now().UnixNano())

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	authority := newReloadAuthority(t, tenant)
	writeBundle(t, dir, tenant, "bundle_only", priv, now, allowOrdinaryOrders, authority)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	bundles.Activations = authority.store
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

// writeRollback stages a bundle with an authorization that says ROLLBACK and names the
// bundle currently in force.
//
// A separate helper because a rollback is a separate act: the customer authorizes the
// retreat and says what they are retreating from, rather than the platform inferring it
// from whichever bundles this process has happened to see.
func writeRollback(t *testing.T, dir, tenant, id string, priv ed25519.PrivateKey,
	now time.Time, source string, authority *reloadAuthority, priorBundleID string) {

	t.Helper()
	writeBundle(t, dir, tenant, id, priv, now, source, nil)

	parsed, err := policy.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(parsed, tenant, id, now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	// Signed, because ContentHash is set by signing and an authorization that names no
	// content hash authorizes whatever later takes the bundle's name.
	if err := bundle.Sign(priv, "reload-test", now); err != nil {
		t.Fatalf("sign: %v", err)
	}

	authorization := policy.Authorization{
		SchemaVersion:     policy.AuthorizationSchemaVersion,
		TenantID:          tenant,
		BundleID:          bundle.BundleID,
		BundleContentHash: bundle.ContentHash,
		PriorBundleID:     priorBundleID,
		Action:            policy.ActionRollback,
		Actor:             "reload-test",
		Reason:            "restoring the previous bundle",
		AuthorizedAt:      time.Now().UTC(),
		Nonce:             fmt.Sprintf("rollback_%s_%d", id, time.Now().UnixNano()),
	}
	if err := authorization.Sign(authority.priv, authority.keyID); err != nil {
		t.Fatalf("sign rollback: %v", err)
	}
	raw, err := json.MarshalIndent(authorization, "", "  ")
	if err != nil {
		t.Fatalf("marshal rollback: %v", err)
	}
	path := filepath.Join(dir, tenant+".activation.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write rollback: %v", err)
	}
	stamp := time.Now().Add(time.Duration(len(raw)) * time.Millisecond)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}
