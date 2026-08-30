//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/policy"
)

// F4-B002: which policy is in force is one serialized chain, not a set of accepted
// authorizations.
//
// The signed authorization already carries PriorBundleID and PriorBundleContentHash. The
// production path used neither for an ACTIVATE and compared only the id for a ROLLBACK, so
// what the customer signed — "move from *this* to that" — was verified as "move to that".
//
// Two consequences, both of which let a policy nobody authorized become current:
//
//   - A signed authorization that was never presented stays valid indefinitely. Sign
//     B0→B1, activate B0→B2 instead, then present the old document: its nonce is unused
//     and its signature is good, so B1 takes effect and the customer's actual last
//     decision is silently undone.
//
//   - Two replicas can each accept a different transition from the same predecessor. Both
//     nonces are unique, so both insert, and the tenant's history branches. Which branch
//     wins is then decided by ORDER BY accepted_at over timestamps two gateways generated
//     from their own clocks.

// f4Rig extends the activation rig with what a chain of transitions needs.
type f4Rig struct{ *activationRig }

func newF4Rig(t *testing.T) *f4Rig { return &f4Rig{newActivationRig(t)} }

// activate stages a bundle, authorizes it from the named predecessor, and applies it.
func (r *f4Rig) activate(t *testing.T, id, source string,
	mutate func(*policy.Authorization)) (*policy.Bundle, error) {

	t.Helper()
	bundle := r.stageBundle(t, id, source, policy.StatusActive)
	r.authorize(t, bundle, mutate)

	_, err := r.bundles(t).Active(context.Background(), r.tenant)
	return bundle, err
}

// from returns a mutator that names a predecessor, the way a customer's tooling would.
func from(prior *policy.Bundle) func(*policy.Authorization) {
	return func(a *policy.Authorization) {
		if prior == nil {
			return
		}
		a.PriorBundleID = prior.BundleID
		a.PriorBundleContentHash = prior.ContentHash
	}
}

// F4-POLICY-01: a signed authorization that was overtaken cannot be presented later.
func TestAStaleAuthorizationCannotReorderPolicyHistory(t *testing.T) {
	ctx := context.Background()
	rig := newF4Rig(t)

	b0, err := rig.activate(t, "bundle_f4_b0", activationPolicy, nil)
	if err != nil {
		t.Fatalf("the first activation failed: %v", err)
	}

	// A1 is signed and set aside: B0 → B1, never presented.
	b1 := rig.stageBundle(t, "bundle_f4_b1", stricterPolicy, policy.StatusActive)
	a1 := policy.Authorization{
		SchemaVersion:          policy.AuthorizationSchemaVersion,
		TenantID:               rig.tenant,
		BundleID:               b1.BundleID,
		BundleContentHash:      b1.ContentHash,
		PriorBundleID:          b0.BundleID,
		PriorBundleContentHash: b0.ContentHash,
		Action:                 policy.ActionActivate,
		Actor:                  "ops@example.test",
		AuthorizedAt:           time.Now().UTC(),
		Nonce:                  fmt.Sprintf("nonce_a1_%d", time.Now().UnixNano()),
	}
	if err := a1.Sign(rig.authKey, rig.keyID); err != nil {
		t.Fatalf("sign A1: %v", err)
	}

	// The customer instead activates B0 → B2. That is their latest decision.
	b2, err := rig.activate(t, "bundle_f4_b2", strictestPolicy, from(b0))
	if err != nil {
		t.Fatalf("the second activation failed: %v", err)
	}

	// Now the old document arrives. Nothing about it is forged: the signature verifies,
	// the nonce is unused, and the bundle it names is a bundle the customer approved.
	// What is wrong is the world it describes.
	rig.write(t, rig.tenant+".json", b1)
	rig.write(t, rig.tenant+".activation.json", a1)

	_, err = rig.bundles(t).Active(ctx, rig.tenant)
	current, currentErr := rig.store.Current(ctx, rig.tenant)
	if currentErr != nil {
		t.Fatalf("read current: %v", currentErr)
	}

	if current.BundleID != b2.BundleID {
		t.Fatalf("a stale authorization moved the tenant from %s to %s. The customer "+
			"signed \"from B0 to B1\" and the platform read it as \"to B1\": an "+
			"authorization that was overtaken must not be able to undo the decision "+
			"that overtook it.", b2.BundleID, current.BundleID)
	}
	if err == nil {
		t.Error("the stale authorization was accepted without error")
	}
	t.Logf("refused: %v", err)
}

// F4-POLICY-02: an ACTIVATE naming the right predecessor id and the wrong content hash.
//
// The id is a name the customer chooses and can be reused; the content hash is what the
// rules actually are. An authorization that binds only the name authorizes whatever later
// took it.
func TestAnActivationWithTheWrongPredecessorHashIsRefused(t *testing.T) {
	ctx := context.Background()
	rig := newF4Rig(t)

	b0, err := rig.activate(t, "bundle_f4h_b0", activationPolicy, nil)
	if err != nil {
		t.Fatalf("the first activation failed: %v", err)
	}

	_, err = rig.activate(t, "bundle_f4h_b1", stricterPolicy, func(a *policy.Authorization) {
		a.PriorBundleID = b0.BundleID
		a.PriorBundleContentHash = "0000000000000000000000000000000000000000000000000000000000000000"
	})
	if err == nil {
		t.Error("an activation naming the wrong predecessor content was accepted")
	}

	current, _ := rig.store.Current(ctx, rig.tenant)
	if current == nil || current.BundleContentHash != b0.ContentHash {
		t.Fatalf("the tenant moved off %s on an authorization that described a "+
			"predecessor it never had", b0.BundleID)
	}
	t.Logf("refused: %v", err)
}

// F4-POLICY-03: the same, for a rollback.
func TestARollbackWithTheWrongPredecessorHashIsRefused(t *testing.T) {
	ctx := context.Background()
	rig := newF4Rig(t)

	b0, err := rig.activate(t, "bundle_f4r_b0", activationPolicy, nil)
	if err != nil {
		t.Fatalf("the first activation failed: %v", err)
	}
	b1, err := rig.activate(t, "bundle_f4r_b1", stricterPolicy, from(b0))
	if err != nil {
		t.Fatalf("the second activation failed: %v", err)
	}

	// A rollback to B0, naming B1 by id and something else by content.
	_, err = rig.activate(t, "bundle_f4r_b0", activationPolicy, func(a *policy.Authorization) {
		a.Action = policy.ActionRollback
		a.PriorBundleID = b1.BundleID
		a.PriorBundleContentHash = "1111111111111111111111111111111111111111111111111111111111111111"
	})
	if err == nil {
		t.Error("a rollback naming the wrong predecessor content was accepted")
	}

	current, _ := rig.store.Current(ctx, rig.tenant)
	if current == nil || current.BundleContentHash != b1.ContentHash {
		t.Fatalf("the rollback took effect from a predecessor the tenant never had")
	}
	t.Logf("refused: %v", err)
}

// F4-POLICY-04: two branches from one predecessor, concurrently. Exactly one commits.
func TestTwoConcurrentBranchesCannotBothCommit(t *testing.T) {
	ctx := context.Background()
	rig := newF4Rig(t)

	b0, err := rig.activate(t, "bundle_f4c_b0", activationPolicy, nil)
	if err != nil {
		t.Fatalf("the first activation failed: %v", err)
	}

	// Two independent providers over one database, which is what two gateway replicas
	// are. Each stages its own bundle in its own directory and authorizes it from B0.
	branch := func(t *testing.T, id, source string) (*policy.Bundle, policy.Authorization) {
		t.Helper()
		bundle := rig.stageBundle(t, id, source, policy.StatusActive)
		a := policy.Authorization{
			SchemaVersion:          policy.AuthorizationSchemaVersion,
			TenantID:               rig.tenant,
			BundleID:               bundle.BundleID,
			BundleContentHash:      bundle.ContentHash,
			PriorBundleID:          b0.BundleID,
			PriorBundleContentHash: b0.ContentHash,
			Action:                 policy.ActionActivate,
			Actor:                  "ops@example.test",
			AuthorizedAt:           time.Now().UTC(),
			Nonce:                  fmt.Sprintf("nonce_%s_%d", id, time.Now().UnixNano()),
		}
		if err := a.Sign(rig.authKey, rig.keyID); err != nil {
			t.Fatalf("sign %s: %v", id, err)
		}
		return bundle, a
	}

	left, aLeft := branch(t, "bundle_f4c_left", stricterPolicy)
	right, aRight := branch(t, "bundle_f4c_right", strictestPolicy)

	at := time.Now().UTC()
	results := make([]error, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, pair := range []struct {
		bundle *policy.Bundle
		auth   policy.Authorization
	}{{left, aLeft}, {right, aRight}} {
		wg.Add(1)
		go func(i int, bundle *policy.Bundle, a policy.Authorization) {
			defer wg.Done()
			<-start
			_, err := rig.store.Accept(ctx, a, bundle, activationEventFor(a, bundle, at), at)
			results[i] = err
		}(i, pair.bundle, pair.auth)
	}
	close(start)
	wg.Wait()

	committed := 0
	for _, err := range results {
		if err == nil {
			committed++
		}
	}
	t.Logf("results: %v / %v", results[0], results[1])

	if committed != 1 {
		t.Fatalf("%d of two branches from one predecessor committed. A tenant's policy "+
			"history is one chain: two accepted transitions from the same predecessor "+
			"leave the platform enforcing whichever a timestamp comparison happens to "+
			"prefer, and the losing customer decision silently disappears.", committed)
	}

	// And the loser left nothing behind: no evidence, and no transition row.
	// In one transaction with the tenant set: a set_config on the pool applies to
	// whichever connection took it, and the count may take another and read zero through
	// row level security — which looks exactly like the rows not existing.
	rows := countAsTenant(t, rig.tenant,
		`SELECT count(*) FROM policy_activations WHERE tenant_id = $1`)
	if rows != 2 {
		t.Errorf("%d transition rows; the first activation and one branch make two", rows)
	}

	chain, err := rig.evidence.ByAggregate(ctx, rig.tenant, left.BundleID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	other, err := rig.evidence.ByAggregate(ctx, rig.tenant, right.BundleID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if len(chain)+len(other) != 1 {
		t.Errorf("the losing branch left %d evidence events; a transition that did not "+
			"happen must not be recorded as one", len(chain)+len(other)-1)
	}
}

// F4-POLICY-06: which policy is current does not depend on gateway clocks.
//
// Two replicas, one two hours ahead and one two hours behind, applying transitions in a
// known order. Ordering by a gateway-supplied accepted_at makes the later transition look
// older, and the answer to "what is in force" changes with whose clock is wrong.
func TestCurrentPolicyIsDeterministicUnderClockSkew(t *testing.T) {
	ctx := context.Background()
	rig := newF4Rig(t)

	b0, err := rig.activate(t, "bundle_f4s_b0", activationPolicy, nil)
	if err != nil {
		t.Fatalf("the first activation failed: %v", err)
	}

	// The second transition is applied by a replica whose clock is two hours behind.
	b1 := rig.stageBundle(t, "bundle_f4s_b1", stricterPolicy, policy.StatusActive)
	behind := time.Now().UTC().Add(-2 * time.Hour)
	a1 := policy.Authorization{
		SchemaVersion:          policy.AuthorizationSchemaVersion,
		TenantID:               rig.tenant,
		BundleID:               b1.BundleID,
		BundleContentHash:      b1.ContentHash,
		PriorBundleID:          b0.BundleID,
		PriorBundleContentHash: b0.ContentHash,
		Action:                 policy.ActionActivate,
		Actor:                  "ops@example.test",
		AuthorizedAt:           behind,
		Nonce:                  fmt.Sprintf("nonce_skew_%d", time.Now().UnixNano()),
	}
	if err := a1.Sign(rig.authKey, rig.keyID); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if _, err := rig.store.Accept(ctx, a1, b1,
		activationEventFor(a1, b1, behind), behind); err != nil {
		t.Fatalf("the skewed replica's transition was refused: %v", err)
	}

	current, err := rig.store.Current(ctx, rig.tenant)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if current.BundleID != b1.BundleID {
		t.Fatalf("current is %s; the last accepted transition was to %s. Ordering by a "+
			"timestamp the gateway generated means a replica with a slow clock can make "+
			"its own change invisible — and an operator's rollback is exactly the change "+
			"most likely to be applied from whichever host is at hand.",
			current.BundleID, b1.BundleID)
	}
}

// F4-POLICY-07: a restart reads the pointer from the database, not from a file.
func TestARestartReadsCurrentFromTheDatabase(t *testing.T) {
	ctx := context.Background()
	rig := newF4Rig(t)

	b0, err := rig.activate(t, "bundle_f4rs_b0", activationPolicy, nil)
	if err != nil {
		t.Fatalf("the first activation failed: %v", err)
	}
	b1, err := rig.activate(t, "bundle_f4rs_b1", stricterPolicy, from(b0))
	if err != nil {
		t.Fatalf("the second activation failed: %v", err)
	}

	// A fresh provider with no memory of any of it.
	fresh, err := rig.store.Current(ctx, rig.tenant)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	if fresh.BundleID != b1.BundleID || fresh.BundleContentHash != b1.ContentHash {
		t.Fatalf("a process starting now reads %s as current; the last accepted "+
			"transition was to %s", fresh.BundleID, b1.BundleID)
	}
}

// activationEventFor builds the evidence event the store writes with a transition.
func activationEventFor(a policy.Authorization, b *policy.Bundle, at time.Time) evidence.Event {
	name := evidence.PolicyBundleActivated
	if a.Action == policy.ActionRollback {
		name = evidence.PolicyBundleRolledBack
	}
	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_%s", b.TenantID, a.Nonce),
		EventName:     name,
		TenantID:      b.TenantID,
		AggregateID:   b.BundleID,
		CorrelationID: b.BundleID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      1,
		Payload: map[string]any{
			"bundle_id": b.BundleID, "content_hash": b.ContentHash,
			"action": string(a.Action), "actor": a.Actor, "nonce": a.Nonce,
		},
	}
}

// strictestPolicy is a third distinct bundle, so a branch test has two different targets.
const strictestPolicy = `
version: 1
policy: pol_activation
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 250
`
