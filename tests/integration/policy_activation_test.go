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
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/policy"
)

// T3-B002 and T3-B003: a bundle enforces only when the customer authorized it, and the
// transition is durable before anything changes.
//
// The bundle's signature covers its rules and deliberately excludes its activation
// block, so promotion was unauthorized: anyone who could edit the file could take a
// correctly signed SHADOW bundle, change one word to ACTIVE, and put it into force
// without the customer's key. The signature still verified, because the rules had not
// changed.

// activationRig is one tenant with an activation key registered and a bundle directory.
type activationRig struct {
	dir       string
	tenant    string
	store     *policy.ActivationStore
	evidence  *evidence.Store
	bundleKey ed25519.PrivateKey
	bundlePub ed25519.PublicKey
	authKey   ed25519.PrivateKey
	keyID     string
}

func newActivationRig(t *testing.T) *activationRig {
	t.Helper()
	pool := idemPool(t)
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_act_%d", now.UnixNano())

	bundlePub, bundlePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("bundle keygen: %v", err)
	}
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("activation keygen: %v", err)
	}

	store := policy.NewActivationStore(pool)
	if _, err := store.RegisterKey(context.Background(), policy.ActivationKey{
		TenantID: tenant, KeyID: "act_key_1",
		PublicKey: authPub, Holder: "ops@example.test",
		Status: "ACTIVE", ValidFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("register activation key: %v", err)
	}

	return &activationRig{
		dir: t.TempDir(), tenant: tenant, store: store,
		evidence:  evidence.NewStore(pool),
		bundleKey: bundlePriv, bundlePub: bundlePub,
		authKey: authPriv, keyID: "act_key_1",
	}
}

func (r *activationRig) bundles(t *testing.T) *gateway.FileBundles {
	t.Helper()
	b, err := gateway.NewFileBundles(r.dir, hex.EncodeToString(r.bundlePub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	b.Activations = r.store
	return b
}

// stageBundle writes a signed bundle at the given lifecycle status.
func (r *activationRig) stageBundle(t *testing.T, id, source string, status policy.Status) *policy.Bundle {
	t.Helper()
	now := time.Now().UTC()

	parsed, err := policy.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(parsed, r.tenant, id, now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := bundle.Sign(r.bundleKey, "activation-test", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, now, "activation-test"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
		if stage == status {
			break
		}
	}

	r.write(t, r.tenant+".json", bundle)
	return bundle
}

// authorize writes a signed activation authorization for a bundle.
func (r *activationRig) authorize(t *testing.T, b *policy.Bundle,
	mutate func(*policy.Authorization)) policy.Authorization {

	t.Helper()
	a := policy.Authorization{
		SchemaVersion:     policy.AuthorizationSchemaVersion,
		TenantID:          r.tenant,
		BundleID:          b.BundleID,
		BundleContentHash: b.ContentHash,
		Action:            policy.ActionActivate,
		Actor:             "ops@example.test",
		Reason:            "approved after shadow validation",
		AuthorizedAt:      time.Now().UTC(),
		Nonce:             fmt.Sprintf("nonce_%d", time.Now().UnixNano()),
	}
	if mutate != nil {
		mutate(&a)
	}
	if err := a.Sign(r.authKey, r.keyID); err != nil {
		t.Fatalf("sign authorization: %v", err)
	}
	r.write(t, r.tenant+".activation.json", a)
	return a
}

func (r *activationRig) write(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	path := filepath.Join(r.dir, name)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	// Modification times have coarse resolution on some filesystems; move it forward
	// explicitly so a change is visible rather than depending on the clock.
	stamp := time.Now().Add(time.Duration(len(raw)) * time.Millisecond)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}

// rewrite edits a staged file in place without re-signing it.
func (r *activationRig) rewrite(t *testing.T, name string, edit func(map[string]any)) {
	t.Helper()
	path := filepath.Join(r.dir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	edit(document)
	r.write(t, name, document)
}

const activationPolicy = `
version: 1
policy: pol_activation
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 1000000
`

const stricterPolicy = `
version: 1
policy: pol_activation
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 1
`

// T3-POLICY-01: promoting a bundle by editing its activation status is refused.
func TestEditingActivationStatusDoesNotActivate(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)

	// A bundle that is genuinely in force, so the refusal below is about the second one
	// rather than about a gateway that refuses everything.
	first := r.stageBundle(t, "bundle_in_force", activationPolicy, policy.StatusActive)
	r.authorize(t, first, nil)

	bundles := r.bundles(t)
	inForce, err := bundles.Active(ctx, r.tenant)
	if err != nil {
		t.Fatalf("the authorized bundle did not take effect: %v", err)
	}
	if inForce.BundleID != "bundle_in_force" {
		t.Fatalf("in force = %s", inForce.BundleID)
	}

	// Now the attack. A correctly signed SHADOW bundle, promoted by editing one word.
	// Its policy signature still verifies, because the rules did not change.
	r.stageBundle(t, "bundle_shadow", stricterPolicy, policy.StatusShadow)
	r.rewrite(t, r.tenant+".json", func(document map[string]any) {
		activation, ok := document["activation"].(map[string]any)
		if !ok {
			t.Fatal("the staged bundle has no activation block")
		}
		activation["status"] = string(policy.StatusActive)
		activation["activated_by"] = "attacker"
	})

	after, err := bundles.Active(ctx, r.tenant)
	if err != nil {
		t.Logf("refused: %v", err)
	}
	if after != nil && after.BundleID == "bundle_shadow" {
		t.Fatal("a SHADOW bundle was promoted into production by editing its activation " +
			"metadata. The policy signature covers the rules and not the promotion, so " +
			"anyone who can write the file can decide what the platform enforces " +
			"(INV-009, INV-010).")
	}
	if after == nil || after.BundleID != "bundle_in_force" {
		t.Errorf("the previously authorized bundle is no longer in force: %v", after)
	}
}

// T3-POLICY-02: editing the authorization without re-signing it is refused.
func TestEditingTheAuthorizationIsRefused(t *testing.T) {
	ctx := context.Background()

	fields := []struct {
		name string
		edit func(map[string]any)
	}{
		{"actor", func(d map[string]any) { d["actor"] = "somebody-else" }},
		{"reason", func(d map[string]any) { d["reason"] = "no reason at all" }},
		{"authorized_at", func(d map[string]any) { d["authorized_at"] = "2020-01-01T00:00:00Z" }},
		{"bundle_content_hash", func(d map[string]any) { d["bundle_content_hash"] = strings.Repeat("0", 64) }},
		{"prior_bundle_id", func(d map[string]any) { d["prior_bundle_id"] = "bundle_invented" }},
	}

	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			r := newActivationRig(t)
			bundle := r.stageBundle(t, "bundle_edit", activationPolicy, policy.StatusActive)
			r.authorize(t, bundle, nil)
			r.rewrite(t, r.tenant+".activation.json", field.edit)

			if _, err := r.bundles(t).Active(ctx, r.tenant); err == nil {
				t.Errorf("an authorization with %s edited after signing was accepted", field.name)
			} else {
				t.Logf("refused: %v", err)
			}
		})
	}
}

// T3-POLICY-03: one tenant cannot activate another's bundle.
func TestAnAuthorizationCannotCrossTenants(t *testing.T) {
	ctx := context.Background()
	a := newActivationRig(t)
	b := newActivationRig(t)

	bundle := b.stageBundle(t, "bundle_theirs", activationPolicy, policy.StatusActive)

	// Tenant A's key signs an authorization for tenant B's bundle, and it is staged in
	// tenant B's directory under tenant B's name.
	authorization := policy.Authorization{
		SchemaVersion:     policy.AuthorizationSchemaVersion,
		TenantID:          a.tenant,
		BundleID:          bundle.BundleID,
		BundleContentHash: bundle.ContentHash,
		Action:            policy.ActionActivate,
		Actor:             "attacker@example.test",
		AuthorizedAt:      time.Now().UTC(),
		Nonce:             fmt.Sprintf("cross_%d", time.Now().UnixNano()),
	}
	if err := authorization.Sign(a.authKey, a.keyID); err != nil {
		t.Fatalf("sign: %v", err)
	}
	b.write(t, b.tenant+".activation.json", authorization)

	if _, err := b.bundles(t).Active(ctx, b.tenant); err == nil {
		t.Error("an authorization issued by another tenant activated this tenant's policy")
	} else {
		t.Logf("refused: %v", err)
	}
}

// T3-POLICY-04: a revoked key authorizes nothing.
func TestARevokedKeyCannotActivate(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)

	bundle := r.stageBundle(t, "bundle_revoked", activationPolicy, policy.StatusActive)
	r.authorize(t, bundle, nil)

	// A replacement first. The store refuses to revoke a tenant's last active key, so
	// that it can never be left unable to authorize a policy change — the rollback an
	// incident needs included (ADR-028).
	spare, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if _, err := r.store.RegisterKey(ctx, policy.ActivationKey{
		TenantID: r.tenant, KeyID: "act_key_spare", PublicKey: spare,
		Holder: "ops@example.test", Status: "ACTIVE",
		ValidFrom: time.Now().UTC().Add(-time.Hour),
	}); err != nil {
		t.Fatalf("register the replacement: %v", err)
	}

	if _, err := r.store.RevokeKey(ctx, r.tenant, r.keyID, "security@example.test",
		time.Now().UTC()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := r.bundles(t).Active(ctx, r.tenant); err == nil {
		t.Error("a revoked activation key put a policy into force")
	} else {
		t.Logf("refused: %v", err)
	}
}

// T3-POLICY-05: a captured authorization cannot be presented twice.
func TestAReplayedAuthorizationIsRefused(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)

	first := r.stageBundle(t, "bundle_replay_a", activationPolicy, policy.StatusActive)
	authorization := r.authorize(t, first, nil)

	bundles := r.bundles(t)
	if _, err := bundles.Active(ctx, r.tenant); err != nil {
		t.Fatalf("first activation: %v", err)
	}

	// A different bundle, and the same authorization document replayed with only its
	// bundle fields updated — which invalidates the signature — is not the interesting
	// case. The interesting one is the original authorization presented again after the
	// customer has moved on, which is what a captured file allows.
	second := r.stageBundle(t, "bundle_replay_b", stricterPolicy, policy.StatusActive)
	r.authorize(t, second, from(first))
	if _, err := bundles.Active(ctx, r.tenant); err != nil {
		t.Fatalf("second activation: %v", err)
	}

	// Put the first bundle and its original authorization back.
	r.write(t, r.tenant+".json", first)
	r.write(t, r.tenant+".activation.json", authorization)

	after, err := bundles.Active(ctx, r.tenant)
	if err == nil && after.BundleID == "bundle_replay_a" {
		t.Error("a previously accepted authorization was replayed and re-activated an " +
			"older bundle; the nonce must make that a conflict")
	} else {
		t.Logf("refused: %v", err)
	}
	if after == nil || after.BundleID != "bundle_replay_b" {
		t.Errorf("what the customer last authorized is no longer in force: %v", after)
	}
}

// T3-POLICY-06: a rollback is authorized in its own right and names both sides.
func TestARollbackIsSeparatelyAuthorized(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)
	bundles := r.bundles(t)

	first := r.stageBundle(t, "bundle_v1", activationPolicy, policy.StatusActive)
	r.authorize(t, first, nil)
	if _, err := bundles.Active(ctx, r.tenant); err != nil {
		t.Fatalf("activate v1: %v", err)
	}

	second := r.stageBundle(t, "bundle_v2", stricterPolicy, policy.StatusActive)
	r.authorize(t, second, from(first))
	if _, err := bundles.Active(ctx, r.tenant); err != nil {
		t.Fatalf("activate v2: %v", err)
	}

	// The retreat, authorized as a rollback and naming what it is rolling back from.
	r.write(t, r.tenant+".json", first)
	r.authorize(t, first, func(a *policy.Authorization) {
		a.Action = policy.ActionRollback
		a.PriorBundleID = second.BundleID
		a.PriorBundleContentHash = second.ContentHash
		a.Reason = "incident 4471: v2 refused legitimate orders"
	})

	back, err := bundles.Active(ctx, r.tenant)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if back.BundleID != "bundle_v1" {
		t.Fatalf("in force = %s, want bundle_v1", back.BundleID)
	}

	// And it is recorded as a rollback rather than as another activation.
	chain, err := r.evidence.ByAggregate(ctx, r.tenant, "bundle_v1")
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	var rolledBack bool
	for _, e := range chain {
		if e.EventName == evidence.PolicyBundleRolledBack {
			rolledBack = true
			if e.Payload["actor"] != "ops@example.test" {
				t.Errorf("the rollback names %v as its actor", e.Payload["actor"])
			}
			if e.Payload["previous_bundle_id"] != "bundle_v2" {
				t.Errorf("the rollback does not name what it rolled back from: %v",
					e.Payload["previous_bundle_id"])
			}
		}
	}
	if !rolledBack {
		t.Error("the retreat was recorded as an activation; an incident review would " +
			"read it as a release")
	}
}

// A rollback that names the wrong predecessor is refused.
func TestARollbackMustNameWhatIsInForce(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)
	bundles := r.bundles(t)

	first := r.stageBundle(t, "bundle_w1", activationPolicy, policy.StatusActive)
	r.authorize(t, first, nil)
	if _, err := bundles.Active(ctx, r.tenant); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// A refusal leaves the previous policy in force and returns no error — failing live
	// submissions over a badly staged file would be worse than continuing to enforce
	// what the customer last authorized. So the assertion is on what is enforcing, and
	// on the refusal being reported rather than silent.
	var refused []error
	bundles.Report = func(_ string, err error) { refused = append(refused, err) }

	second := r.stageBundle(t, "bundle_w2", stricterPolicy, policy.StatusActive)
	r.authorize(t, second, func(a *policy.Authorization) {
		a.Action = policy.ActionRollback
		a.PriorBundleID = "bundle_that_was_never_in_force"
	})

	after, err := bundles.Active(ctx, r.tenant)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if after.BundleID != "bundle_w1" {
		t.Errorf("in force = %s. A rollback naming a predecessor that was never in "+
			"force was accepted, and an audit trail built from it would be fiction.",
			after.BundleID)
	}
	if len(refused) == 0 {
		t.Error("the refusal was silent; an operator would discover days later that " +
			"what they staged never took effect")
	} else {
		t.Logf("refused: %v", refused[0])
	}
}

// T3-B003: a restart does not invent a customer action, and replicas agree.
func TestARestartDoesNotFabricateAnActivation(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)

	bundle := r.stageBundle(t, "bundle_restart", activationPolicy, policy.StatusActive)
	r.authorize(t, bundle, nil)

	if _, err := r.bundles(t).Active(ctx, r.tenant); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// A fresh provider is a fresh process: no memory of what was in force. It reads the
	// same files and must not record a second activation, because the customer did
	// nothing. This used to be decided by a process-local set of bundle ids.
	for i := range 3 {
		if _, err := r.bundles(t).Active(ctx, r.tenant); err != nil {
			t.Fatalf("restart %d: %v", i, err)
		}
	}

	chain, err := r.evidence.ByAggregate(ctx, r.tenant, "bundle_restart")
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	activations := 0
	for _, e := range chain {
		if e.EventName == evidence.PolicyBundleActivated {
			activations++
		}
	}
	if activations != 1 {
		t.Errorf("%d activations recorded for one customer act. A process that did not "+
			"witness the earlier activation must not report one: an evidence timeline "+
			"describes customer actions, not process observations mistaken for them.",
			activations)
	}
}

// T3-B003: if the transition cannot be recorded, the old policy stays in force.
func TestAFailedTransitionLeavesTheOldPolicyEnforcing(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)
	bundles := r.bundles(t)

	first := r.stageBundle(t, "bundle_durable", activationPolicy, policy.StatusActive)
	r.authorize(t, first, nil)
	if _, err := bundles.Active(ctx, r.tenant); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// The store goes away, which is what an evidence outage looks like from here.
	// Enforcement must not follow the file.
	second := r.stageBundle(t, "bundle_durable_v2", stricterPolicy, policy.StatusActive)
	r.authorize(t, second, nil)
	bundles.Activations = nil

	after, err := bundles.Active(ctx, r.tenant)
	if err == nil {
		t.Log("no error surfaced")
	}
	if after == nil || after.BundleID != "bundle_durable" {
		t.Errorf("in force = %v; a policy whose authorization could not be recorded was "+
			"applied anyway, so the platform can enforce a change nobody can attribute",
			after)
	}
}

// An activation event has to reach the bus, not merely the table.
//
// The transition wrote the event's payload into the outbox instead of the whole event,
// so every activation queued as a bare payload, failed validation on
// "schema_version: required", and stayed queued forever. The events were recorded
// correctly and none of them ever reached the analytical plane — a producer that exists,
// runs, and whose output nobody downstream can read.
func TestAnActivationEventIsPublishable(t *testing.T) {
	ctx := context.Background()
	r := newActivationRig(t)

	bundle := r.stageBundle(t, "bundle_publishable", activationPolicy, policy.StatusActive)
	r.authorize(t, bundle, nil)
	if _, err := r.bundles(t).Active(ctx, r.tenant); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// Read back the way the publisher reads: the queued row, unmarshalled into an event
	// and validated. Checking that a row exists would have passed against the defect.
	drainer := evidence.NewStore(outboxPool(t))
	queued, err := drainer.Claim(ctx, 2000, "activation-test", time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	found := false
	for _, entry := range queued {
		if entry.TenantID != r.tenant {
			continue
		}
		found = true
		var event evidence.Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			t.Fatalf("the queued activation is not an event: %v", err)
		}
		if err := event.Validate(); err != nil {
			t.Errorf("the queued activation would be rejected by the publisher: %v", err)
		}
		if event.EventName != evidence.PolicyBundleActivated {
			t.Errorf("queued %s", event.EventName)
		}
		if event.Payload["actor"] != "ops@example.test" {
			t.Errorf("the queued event lost its actor: %v", event.Payload)
		}
	}
	if !found {
		t.Error("the activation was recorded and never queued; the analytical plane " +
			"would never learn that enforcement changed")
	}
}
