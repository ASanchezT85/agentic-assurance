//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/policy"
)

// Bootstrapping and extending policy authority, over the API, against the real database.
//
// The unit tests hold the handler to its security properties with an in-memory store.
// This one runs the whole thing against PostgreSQL, because two of the properties are the
// database's: the nonce is a primary key, so a replay is refused by a constraint on every
// replica rather than by whichever process remembered; and the key row and its grant
// record are written in one transaction, so a key able to decide which policy enforces
// cannot become usable through a commit that did not also record who granted it.
//
// The question a customer asks: can I take custody of my own policy authority, and extend
// it, without anyone touching the database? Until this endpoint existed the answer was no.

type activationAPIRig struct {
	tenant   string
	store    *policy.ActivationStore
	evidence *evidence.Store
	mux      *http.ServeMux
}

const (
	activationAPIToken = "activation-api-token-of-thirty-two-plus-chars"
	submitAPIToken     = "submit-api-token-of-thirty-two-plus-characters"
)

func newActivationAPIRig(t *testing.T) *activationAPIRig {
	t.Helper()
	pool := idemPool(t)
	tenant := fmt.Sprintf("tenant_actapi_%d", time.Now().UnixNano())

	creds, err := identity.ParseCredentials(fmt.Sprintf(
		"svc_policy@%s=%s,svc_agent@%s=%s",
		tenant, activationAPIToken, tenant, submitAPIToken))
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	creds.AllowKeyRegistrars("svc_agent")
	creds.AllowActivationKeyRegistrars("svc_policy")

	store := policy.NewActivationStore(pool)
	events := evidence.NewStore(pool)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/policy-activation-keys/revoke",
		gateway.RevokeActivationKeyHandler(store, events, creds, nil, nil))
	mux.HandleFunc("POST /v1/policy-activation-keys",
		gateway.RegisterActivationKeyHandler(store, events, creds, nil, nil))

	return &activationAPIRig{tenant: tenant, store: store, evidence: events, mux: mux}
}

func (r *activationAPIRig) post(t *testing.T, path string, body any, token string) (int, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)

	payload, _ := io.ReadAll(rec.Body)
	var decoded map[string]any
	_ = json.Unmarshal(payload, &decoded)
	return rec.Code, decoded
}

func TestPolicyAuthorityIsBootstrappedAndThenExtendsItself(t *testing.T) {
	rig := newActivationAPIRig(t)
	ctx := context.Background()

	// 1. The bootstrap. No database access, and only because this tenant holds no key.
	firstPub, firstPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	status, body := rig.post(t, "/v1/policy-activation-keys", map[string]any{
		"key_id": "act_api_1", "public_key": hex.EncodeToString(firstPub),
		"holder": "risk@example.test", "actor": "ops@example.test",
	}, activationAPIToken)
	if status != http.StatusCreated {
		t.Fatalf("the bootstrap answered %d: %v", status, body)
	}
	if body["bootstrap"] != true {
		t.Errorf("the response does not say this was a bootstrap: %v", body)
	}

	// It is a bootstrap exactly once. A second unsigned registration is the escalation
	// this design exists to refuse: the platform's operator does not add to a customer's
	// policy authority once the customer has some.
	secondPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	status, body = rig.post(t, "/v1/policy-activation-keys", map[string]any{
		"key_id": "act_api_2", "public_key": hex.EncodeToString(secondPub),
		"holder": "risk@example.test", "actor": "ops@example.test",
	}, activationAPIToken)
	if status != http.StatusBadRequest {
		t.Fatalf("a second unsigned registration answered %d: %v", status, body)
	}

	// 2. The customer's own key signs for the second one.
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         rig.tenant,
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_api_2",
		SubjectPublicKey: hex.EncodeToString(secondPub),
		SubjectHolder:    "treasury@example.test",
		Actor:            "risk@example.test",
		Reason:           "second signer for four-eyes policy changes",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            fmt.Sprintf("nonce_%d", time.Now().UnixNano()),
	}
	if err := auth.Sign(firstPriv, "act_api_1"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	status, body = rig.post(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationAPIToken)
	if status != http.StatusCreated {
		t.Fatalf("an authorized registration answered %d: %v", status, body)
	}
	if body["authorized_by_key_id"] != "act_api_1" {
		t.Errorf("the response does not name the authorizing key: %v", body)
	}

	// 3. Replay. The nonce is a primary key, so this is refused by the database.
	status, body = rig.post(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationAPIToken)
	if status != http.StatusConflict {
		t.Fatalf("a replayed authorization answered %d: %v", status, body)
	}

	// 4. The registration is in the record, and says who granted it. An evidence chain
	//    that cannot say who authorized the key that authorized a policy ends one step
	//    short of the question an investigation asks.
	chain, err := rig.evidence.ByAggregate(ctx, rig.tenant, "act_api_2")
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	var registered *evidence.Event
	for i, e := range chain {
		if e.EventName == evidence.ActivationKeyRegistered {
			registered = &chain[i]
		}
	}
	if registered == nil {
		t.Fatalf("no %s in the record; the set of keys able to decide which policy "+
			"enforces changed and nothing wrote it down", evidence.ActivationKeyRegistered)
	}
	if registered.Payload["authorized_by_key_id"] != "act_api_1" {
		t.Errorf("the record does not name the authorizing key: %v", registered.Payload)
	}
	if registered.Payload["bootstrap"] != false {
		t.Errorf("an authorized registration is recorded as a bootstrap: %v", registered.Payload)
	}

	// 5. The new key really can authorize: it activates a bundle, end to end. A key that
	//    registered and cannot be used would be a registration of an intention.
	activateWith(t, rig, secondPub, "act_api_2")

	// 6. And the first key can now be retired, because a second one exists.
	status, body = rig.post(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_api_1", "revoked_by": "security@example.test", "reason": "rotation",
	}, activationAPIToken)
	if status != http.StatusOK {
		t.Fatalf("revocation answered %d: %v", status, body)
	}

	// The revoked key no longer grants authority.
	thirdPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	after := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         rig.tenant,
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_api_3",
		SubjectPublicKey: hex.EncodeToString(thirdPub),
		SubjectHolder:    "whoever",
		Actor:            "whoever",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            fmt.Sprintf("nonce_after_%d", time.Now().UnixNano()),
	}
	if err := after.Sign(firstPriv, "act_api_1"); err != nil {
		t.Fatalf("sign: %v", err)
	}
	status, body = rig.post(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": after}, activationAPIToken)
	if status != http.StatusForbidden {
		t.Errorf("a revoked key granted authority to a new one (%d %v); a revocation "+
			"that does not stop that is a record of an intention", status, body)
	}

	// 7. And the last remaining key cannot be revoked, or the tenant could never
	//    authorize another policy change — including the rollback an incident needs.
	status, body = rig.post(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_api_2", "revoked_by": "security@example.test",
	}, activationAPIToken)
	if status != http.StatusConflict {
		t.Errorf("the tenant's last activation key was revoked: %d %v", status, body)
	}
}

// activateWith proves the newly registered key can do the thing it was registered for.
func activateWith(t *testing.T, rig *activationAPIRig, public ed25519.PublicKey, keyID string) {
	t.Helper()

	key, err := rig.store.Key(context.Background(), rig.tenant, keyID)
	if err != nil {
		t.Fatalf("read back %s: %v", keyID, err)
	}
	if !key.PublicKey.Equal(public) {
		t.Fatalf("the stored public key is not the one registered; a key registered " +
			"under a name that resolves to different bytes would authorize policy " +
			"nobody signed for")
	}
	if err := key.Usable(time.Now().UTC()); err != nil {
		t.Fatalf("the freshly registered key is not usable: %v", err)
	}
}

// A submission credential cannot bootstrap policy authority, against the real store.
func TestBootstrappingPolicyAuthorityNeedsItsOwnPrivilege(t *testing.T) {
	rig := newActivationAPIRig(t)

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	// svc_agent holds the agent-key registrar privilege in this rig, deliberately: the
	// smaller privilege must not carry the larger.
	status, body := rig.post(t, "/v1/policy-activation-keys", map[string]any{
		"key_id": "act_intruder", "public_key": hex.EncodeToString(public),
		"holder": "whoever", "actor": "whoever",
	}, submitAPIToken)

	if status != http.StatusForbidden {
		t.Fatalf("an agent-key registrar bootstrapped policy authority: %d %v", status, body)
	}
	if _, err := rig.store.Key(context.Background(), rig.tenant, "act_intruder"); err == nil {
		t.Error("the key was stored anyway")
	}
}
