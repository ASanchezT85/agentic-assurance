//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/policy"
)

// The addendum to the fourth audit, on the key-registration endpoints.
//
// The design says a tenant's first activation key is a bootstrap, possible exactly once.
// It was implemented as a read followed by a write: count the tenant's active keys, and if
// the count is zero take the bootstrap path. Between the read and the write anything can
// happen, and what an operator credential can do with that is hold more policy authority
// than the model says it ever receives.
//
// Three more of the same kind: a signed authorization that does not cover the validity
// window the key is registered with, a signer whose revocation lands between verification
// and commit, and a "last active key" guard that counts keys which cannot authorize
// anything.

// F4-K001: two concurrent first-key bootstraps. Exactly one may commit.
func TestOnlyOneBootstrapCanEverCommit(t *testing.T) {
	ctx := context.Background()
	rig := newActivationAPIRig(t)

	type result struct {
		status int
		body   map[string]any
	}
	results := make([]result, 2)
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := range 2 {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("keygen: %v", err)
		}
		wg.Add(1)
		go func(i int, public ed25519.PublicKey) {
			defer wg.Done()
			<-start
			status, body := rig.post(t, "/v1/policy-activation-keys", map[string]any{
				"key_id":     fmt.Sprintf("act_race_%d", i),
				"public_key": hex.EncodeToString(public),
				"holder":     "risk@example.test",
				"actor":      "ops@example.test",
			}, activationAPIToken)
			results[i] = result{status, body}
		}(i, public)
	}
	close(start)
	wg.Wait()

	created := 0
	for _, r := range results {
		if r.status == http.StatusCreated {
			created++
		}
	}
	t.Logf("results: %d %v / %d %v", results[0].status, results[0].body["key_id"],
		results[1].status, results[1].body["key_id"])

	if created != 1 {
		t.Fatalf("%d of two concurrent bootstraps committed. \"The first key, exactly "+
			"once\" has to be an invariant the database holds under concurrency: an "+
			"operator who can turn a read-before-write into two keys keeps more policy "+
			"authority than the design says the bootstrap ever grants.", created)
	}

	// One key, and one bootstrap grant.
	keys := rig.countKeys(t)
	if keys != 1 {
		t.Errorf("the tenant holds %d activation keys after two bootstraps", keys)
	}
	if grants := rig.countBootstrapGrants(t); grants != 1 {
		t.Errorf("%d bootstrap grants recorded", grants)
	}

	// F4-K001, second half: the bootstrap does not reopen when every key becomes
	// unusable. A tenant that has bootstrapped has bootstrapped.
	only := results[0].body["key_id"]
	if results[0].status != http.StatusCreated {
		only = results[1].body["key_id"]
	}
	rig.expire(t, fmt.Sprint(only))

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	status, body := rig.post(t, "/v1/policy-activation-keys", map[string]any{
		"key_id": "act_second_bootstrap", "public_key": hex.EncodeToString(public),
		"holder": "risk@example.test", "actor": "ops@example.test",
	}, activationAPIToken)

	if status == http.StatusCreated {
		t.Errorf("the bootstrap reopened once the tenant's only key expired (%v). "+
			"Bootstrap is a one-time authority event; it is not \"nobody can sign right "+
			"now\", or an operator waits for an expiry and mints a signer.", body)
	}
	_ = ctx
}

// F4-K002: the customer's signature covers the key's validity window.
//
// valid_until lived in the HTTP wrapper, outside the signed document. The registrar could
// therefore shorten or extend the authority of a key the customer authorized — the
// signature stayed valid because the field was not in the signed bytes. A signed document
// that is not a complete statement of the authority granted is not an authorization.
func TestTheSignedAuthorizationCoversTheKeysValidity(t *testing.T) {
	rig := newActivationAPIRig(t)
	_, firstPriv := rig.bootstrap(t, "act_v_1")

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	until := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	auth := policy.KeyAuthorization{
		SchemaVersion:     policy.KeyAuthorizationSchemaVersion,
		TenantID:          rig.tenant,
		Action:            policy.ActionRegisterKey,
		SubjectKeyID:      "act_v_2",
		SubjectPublicKey:  hex.EncodeToString(public),
		SubjectHolder:     "treasury@example.test",
		SubjectValidUntil: &until,
		Actor:             "risk@example.test",
		AuthorizedAt:      time.Now().UTC(),
		Nonce:             fmt.Sprintf("nonce_v_%d", time.Now().UnixNano()),
	}
	if err := auth.Sign(firstPriv, "act_v_1"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	// The presenter tries to extend it by a year in the unsigned wrapper.
	stretched := time.Now().UTC().Add(365 * 24 * time.Hour)
	status, body := rig.post(t, "/v1/policy-activation-keys", map[string]any{
		"authorization": auth,
		"valid_until":   stretched,
	}, activationAPIToken)

	if status == http.StatusCreated {
		stored, err := rig.store.Key(context.Background(), rig.tenant, "act_v_2")
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		if stored.ValidUntil != nil && stored.ValidUntil.After(until.Add(time.Minute)) {
			t.Fatalf("the key was registered as valid until %s; the customer signed for "+
				"%s. An unsigned field of the request changed the authority the "+
				"signature granted.", stored.ValidUntil.UTC(), until)
		}
	}
	t.Logf("answered %d %v", status, body["error"])

	// And the signed window is what is stored, when nothing tries to override it.
	auth.Nonce = fmt.Sprintf("nonce_v2_%d", time.Now().UnixNano())
	auth.SubjectKeyID = "act_v_3"
	if err := auth.Sign(firstPriv, "act_v_1"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	status, body = rig.post(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationAPIToken)
	if status != http.StatusCreated {
		t.Fatalf("a signed registration with a validity window was refused: %d %v", status, body)
	}
	stored, err := rig.store.Key(context.Background(), rig.tenant, "act_v_3")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.ValidUntil == nil || !stored.ValidUntil.UTC().Truncate(time.Second).Equal(until) {
		t.Errorf("stored valid_until is %v; the customer signed %s", stored.ValidUntil, until)
	}
}

// F4-K003: the authorizing key's state is revalidated in the transaction that commits.
//
// The handler read the signer, checked Usable, verified the signature, and only then
// entered the registration transaction. A revocation committed in that window left a
// revoked key granting authority anyway — and "a revoked key cannot authorize a further
// key" is exactly the claim the report made.
func TestARevocationBeatsAConcurrentRegistration(t *testing.T) {
	ctx := context.Background()
	rig := newActivationAPIRig(t)
	_, firstPriv := rig.bootstrap(t, "act_toctou_1")

	// A second key, so the first is revocable at all.
	rig.registerSigned(t, firstPriv, "act_toctou_1", "act_toctou_2")

	// Now: revoke the first key, then present an authorization it signed. The
	// authorization was signed while the key was good, which is the whole point — what
	// must be checked is the key's state at the moment the grant commits.
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         rig.tenant,
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_toctou_3",
		SubjectPublicKey: hex.EncodeToString(public),
		SubjectHolder:    "whoever",
		Actor:            "whoever",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            fmt.Sprintf("nonce_toctou_%d", time.Now().UnixNano()),
	}
	if err := auth.Sign(firstPriv, "act_toctou_1"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	if _, err := rig.store.RevokeKey(ctx, rig.tenant, "act_toctou_1",
		"security@example.test", time.Now().UTC()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	status, body := rig.post(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationAPIToken)
	if status == http.StatusCreated {
		t.Fatalf("a key revoked before the grant committed granted policy authority "+
			"anyway: %v", body)
	}
	t.Logf("refused: %d %v", status, body["code"])
}

// F4-K004: the last-key guard counts keys that can actually authorize something.
//
// ActiveKeys and RevokeKey counted status='ACTIVE' AND revoked_at IS NULL, and never
// looked at the validity window. A tenant with one usable key and one expired row read as
// two, so the usable one could be revoked and the tenant was left with nothing able to
// authorize a policy change — including the rollback an incident needs — and no bootstrap
// to fall back on.
func TestTheLastUsableKeyCannotBeRevoked(t *testing.T) {
	ctx := context.Background()
	rig := newActivationAPIRig(t)
	rig.bootstrap(t, "act_usable")

	// A second key that is ACTIVE and expired.
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	expired := time.Now().UTC().Add(-time.Hour)
	if _, err := rig.store.RegisterKey(ctx, policy.ActivationKey{
		TenantID: rig.tenant, KeyID: "act_expired", PublicKey: public,
		Holder: "old@example.test", Status: "ACTIVE",
		ValidFrom: time.Now().UTC().Add(-48 * time.Hour), ValidUntil: &expired,
	}); err != nil {
		t.Fatalf("register the expired key: %v", err)
	}

	status, body := rig.post(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_usable", "revoked_by": "security@example.test",
	}, activationAPIToken)

	if status == http.StatusOK {
		t.Fatalf("the tenant's only usable key was revoked because an expired row was "+
			"counted as available. What is left cannot authorize anything, the bootstrap "+
			"is spent, and the only way back is direct database access — which is the "+
			"state these endpoints exist to remove. (%v)", body)
	}
	t.Logf("refused: %d %v", status, body["error"])

	usable, err := rig.store.Key(ctx, rig.tenant, "act_usable")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := usable.Usable(time.Now().UTC()); err != nil {
		t.Errorf("the tenant's usable key is no longer usable: %v", err)
	}
}

// F4-K005: an unknown key is not reported as revoked, and leaves no evidence saying it was.
func TestRevokingAnUnknownAgentKeyIsNotReportedAsSuccess(t *testing.T) {
	ctx := context.Background()
	rig := newE2ERig(t, time.Now().UTC().Truncate(time.Second))

	agentID := fmt.Sprintf("agent_ghost_%d", time.Now().UnixNano())
	status, body := rig.postJSON(t, "/v1/agent-keys/revoke", map[string]any{
		"agent_id": agentID, "key_id": "key_that_never_existed",
		"revoked_by": "security@example.test",
	}, rig.registrarToken)

	if status == http.StatusOK {
		t.Errorf("revoking a key that does not exist answered 200: %v.\n\nAn operator "+
			"reading that believes a key has been stopped. If they mistyped the agent "+
			"id, the key they meant to revoke is still trusted and nothing said so.", body)
	}

	chain, err := rig.evidence.ByAggregate(ctx, rig.tenant, agentID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	for _, e := range chain {
		if e.EventName == evidence.AgentKeyRevoked {
			t.Errorf("a revocation event was recorded for a key that was never revoked. " +
				"An audit trail that says a key stopped being trusted, when no key " +
				"changed, is worse than no entry at all.")
		}
	}
}

// --- rig helpers ------------------------------------------------------------------

// bootstrap performs the tenant's one bootstrap and returns the key pair.
func (r *activationAPIRig) bootstrap(t *testing.T, keyID string) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	status, body := r.post(t, "/v1/policy-activation-keys", map[string]any{
		"key_id": keyID, "public_key": hex.EncodeToString(public),
		"holder": "risk@example.test", "actor": "ops@example.test",
	}, activationAPIToken)
	if status != http.StatusCreated {
		t.Fatalf("bootstrap answered %d: %v", status, body)
	}
	return public, private
}

// registerSigned adds a key authorized by an existing one.
func (r *activationAPIRig) registerSigned(t *testing.T, signer ed25519.PrivateKey,
	signerKeyID, keyID string) ed25519.PrivateKey {

	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         r.tenant,
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     keyID,
		SubjectPublicKey: hex.EncodeToString(public),
		SubjectHolder:    "treasury@example.test",
		Actor:            "risk@example.test",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            fmt.Sprintf("nonce_%s_%d", keyID, time.Now().UnixNano()),
	}
	if err := auth.Sign(signer, signerKeyID); err != nil {
		t.Fatalf("sign: %v", err)
	}
	status, body := r.post(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationAPIToken)
	if status != http.StatusCreated {
		t.Fatalf("registering %s answered %d: %v", keyID, status, body)
	}
	return private
}

func (r *activationAPIRig) countKeys(t *testing.T) int {
	t.Helper()
	return r.count(t, `SELECT count(*) FROM policy_activation_keys WHERE tenant_id = $1`)
}

func (r *activationAPIRig) countBootstrapGrants(t *testing.T) int {
	t.Helper()
	return r.count(t, `SELECT count(*) FROM policy_activation_key_grants
	                    WHERE tenant_id = $1 AND action = 'BOOTSTRAP_ACTIVATION_KEY'`)
}

func (r *activationAPIRig) count(t *testing.T, query string) int {
	t.Helper()
	ctx := context.Background()
	pool := idemPool(t)
	if _, err := pool.Exec(ctx, `SELECT set_config('app.tenant_id', $1, false)`, r.tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, query, r.tenant).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// expire makes a key unusable without revoking it, which is the state the last-key guard
// used to miss.
func (r *activationAPIRig) expire(t *testing.T, keyID string) {
	t.Helper()
	ctx := context.Background()
	pool := idemPool(t)
	if _, err := pool.Exec(ctx, `SELECT set_config('app.tenant_id', $1, false)`, r.tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE policy_activation_keys SET valid_until = now() - interval '1 hour'
		 WHERE tenant_id = $1 AND key_id = $2`, r.tenant, keyID); err != nil {
		t.Fatalf("expire %s: %v", keyID, err)
	}
}
