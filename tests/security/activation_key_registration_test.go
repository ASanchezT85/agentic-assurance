package security

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// Registering an activation key is the strongest act the API exposes.
//
// An activation key authorizes a policy bundle into force, and a bundle says what every
// agent in the tenant may not do. Whoever can add one can hand themselves the power to
// lift every ceiling the customer set, without ever touching a grant or an agent key.
//
// So the property under test is not "the endpoint works". It is that an operator
// credential can bootstrap the first key and can never mint a second — that a tenant
// which already holds policy authority extends it only by its own signature.

// activationRegistry is the store, in memory.
type activationRegistry struct {
	keys   map[string]policy.ActivationKey
	nonces map[string]bool
	grants []string
	events []evidence.Event
	fail   error
}

func newActivationRegistry() *activationRegistry {
	return &activationRegistry{
		keys: map[string]policy.ActivationKey{}, nonces: map[string]bool{},
	}
}

func (r *activationRegistry) Bootstrapped(context.Context, string) (bool, error) {
	if r.fail != nil {
		return false, r.fail
	}
	for _, g := range r.grants {
		if strings.HasPrefix(g, "BOOTSTRAP_ACTIVATION_KEY|") {
			return true, nil
		}
	}
	// A key registered by something other than this endpoint closes the bootstrap too.
	return len(r.keys) > 0, nil
}

// usable counts the keys that could authorize something now, as the store does.
func (r *activationRegistry) usable(at time.Time) int {
	n := 0
	for _, k := range r.keys {
		if k.Usable(at) == nil {
			n++
		}
	}
	return n
}

func (r *activationRegistry) Key(_ context.Context, tenant, keyID string) (*policy.ActivationKey, error) {
	k, ok := r.keys[keyID]
	if !ok {
		return nil, &policy.ActivationError{
			Code:    "ACTIVATION_KEY_UNKNOWN",
			Message: "no activation key " + keyID + " is registered for this tenant",
		}
	}
	return &k, nil
}

func (r *activationRegistry) RegisterKeyAuthorized(_ context.Context, k policy.ActivationKey,
	nonce, action, actor, signedBy string, _ time.Time, event evidence.Event,
	_ time.Time) (bool, error) {

	if r.fail != nil {
		return false, r.fail
	}
	if r.nonces[nonce] {
		return false, policy.ErrReplayed
	}
	if _, exists := r.keys[k.KeyID]; exists {
		return false, nil
	}
	r.nonces[nonce] = true
	r.keys[k.KeyID] = k
	r.grants = append(r.grants, fmt.Sprintf("%s|%s|%s|%s", action, k.KeyID, actor, signedBy))
	r.events = append(r.events, event)
	return true, nil
}

func (r *activationRegistry) RevokeKey(_ context.Context, _, keyID, by string,
	at time.Time) (bool, error) {

	if r.fail != nil {
		return false, r.fail
	}
	if r.usable(at) <= 1 {
		return false, &policy.ActivationError{
			Code:    "ACTIVATION_KEY_LAST_USABLE",
			Message: "key " + keyID + " is the only usable activation key for this tenant",
		}
	}
	k, ok := r.keys[keyID]
	if !ok || k.Status != "ACTIVE" {
		return false, nil
	}
	k.Status = "REVOKED"
	k.RevokedAt = &at
	k.RevokedBy = by
	r.keys[keyID] = k
	return true, nil
}

const (
	activationRegistrarToken = "activation-registrar-token-thirty-two-plus"
	keyRegistrarToken        = "agent-key-registrar-token-thirty-two-plus"
)

// activationCredentials is one tenant with three callers: one that may bootstrap
// activation keys, one that may only register agent keys, and one that may only submit.
//
// The middle one is the point. The two registrar privileges are different powers, and an
// operator granted the smaller must not hold the larger.
func activationCredentials(t *testing.T) *identity.Credentials {
	t.Helper()
	creds, err := identity.ParseCredentials(fmt.Sprintf(
		"svc_policy@tenant_act=%s,svc_registrar@tenant_act=%s,svc_agent@tenant_act=%s",
		activationRegistrarToken, keyRegistrarToken, agentToken))
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	creds.AllowKeyRegistrars("svc_registrar")
	creds.AllowIssuers("svc_registrar")
	creds.AllowActivationKeyRegistrars("svc_policy")
	return creds
}

func activationRequest(t *testing.T, path string, body map[string]any, token string) *http.Request {
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
	return req
}

func bootstrapBody(t *testing.T, keyID string) (map[string]any, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return map[string]any{
		"key_id":     keyID,
		"public_key": hex.EncodeToString(public),
		"holder":     "risk@example.test",
		"actor":      "ops@example.test",
	}, private
}

// bootstrapped returns a registry holding one active key, and its private half.
func bootstrapped(t *testing.T) (*activationRegistry, ed25519.PrivateKey) {
	t.Helper()
	registry := newActivationRegistry()
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	body, private := bootstrapBody(t, "act_first")
	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys", body, activationRegistrarToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("the bootstrap answered %d: %s", rec.Code, rec.Body.String())
	}
	return registry, private
}

func TestTheFirstActivationKeyIsBootstrappedByANamedOperator(t *testing.T) {
	registry, _ := bootstrapped(t)

	stored, ok := registry.keys["act_first"]
	if !ok {
		t.Fatal("the key was accepted and not stored")
	}
	if stored.TenantID != "tenant_act" {
		// The tenant comes from the credential and never from the request (INV-007).
		t.Errorf("stored under tenant %q", stored.TenantID)
	}
	if len(registry.grants) != 1 || !strings.HasPrefix(registry.grants[0], "BOOTSTRAP_ACTIVATION_KEY|") {
		t.Errorf("the grant record is %v; a bootstrap must be recorded as one, because it "+
			"is the single key in a tenant's history that no customer signature "+
			"authorized", registry.grants)
	}
	if len(registry.events) != 1 ||
		registry.events[0].EventName != evidence.ActivationKeyRegistered {
		t.Fatalf("events: %v", registry.events)
	}
	if registry.events[0].Payload["bootstrap"] != true {
		t.Error("the record does not say this key was bootstrapped rather than authorized")
	}
}

// The whole point of the design: once a tenant has policy authority, the operator
// credential cannot add to it.
func TestAnOperatorCannotMintASecondActivationKey(t *testing.T) {
	registry, _ := bootstrapped(t)
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	body, _ := bootstrapBody(t, "act_second")
	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys", body, activationRegistrarToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an operator credential registered a second activation key: %d %s\n\n"+
			"A key that decides which policy enforces decides what every agent may not "+
			"do. If the platform's own operator can add one to a tenant that already "+
			"holds policy authority, the customer no longer owns that authority "+
			"(INV-009, P-002).", rec.Code, rec.Body.String())
	}
	if _, exists := registry.keys["act_second"]; exists {
		t.Error("the key was stored anyway")
	}
}

func TestASecondActivationKeyIsRegisteredWhenTheFirstSignsForIt(t *testing.T) {
	registry, private := bootstrapped(t)
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         "tenant_act",
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_second",
		SubjectPublicKey: hex.EncodeToString(public),
		SubjectHolder:    "treasury@example.test",
		Actor:            "risk@example.test",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            "nonce_second",
	}
	if err := auth.Sign(private, "act_first"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationRegistrarToken))

	if rec.Code != http.StatusCreated {
		t.Fatalf("a key authorized by the customer's own key was refused: %d %s",
			rec.Code, rec.Body.String())
	}
	if len(registry.grants) != 2 || !strings.Contains(registry.grants[1], "|act_first") {
		t.Errorf("the grant record is %v; it must name the key that authorized this one, "+
			"or the chain from the first key to the current one cannot be followed",
			registry.grants)
	}

	// And the same authorization a second time is a replay, not a second registration.
	replay := httptest.NewRecorder()
	handler(replay, activationRequest(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationRegistrarToken))
	if replay.Code != http.StatusConflict {
		t.Errorf("a replayed authorization answered %d: %s", replay.Code, replay.Body.String())
	}
}

func TestAnAuthorizationSignedByAnUnregisteredKeyIsRefused(t *testing.T) {
	registry, _ := bootstrapped(t)
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	// A perfectly valid signature, by a key nobody registered. This is the shape of the
	// attack: a signature verifies against the key that made it, so the question is
	// never "is this signed" but "is it signed by an authority this tenant granted".
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         "tenant_act",
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_intruder",
		SubjectPublicKey: hex.EncodeToString(public),
		SubjectHolder:    "whoever",
		Actor:            "whoever",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            "nonce_intruder",
	}
	if err := auth.Sign(private, "act_unknown"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationRegistrarToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a key signed by an unregistered key answered %d: %s",
			rec.Code, rec.Body.String())
	}
	if _, exists := registry.keys["act_intruder"]; exists {
		t.Error("the key was stored anyway")
	}
}

func TestAnAuthorizationSignedByARevokedKeyIsRefused(t *testing.T) {
	registry, private := bootstrapped(t)

	// A second key, so the first may be revoked at all.
	second := policy.ActivationKey{
		TenantID: "tenant_act", KeyID: "act_second", Status: "ACTIVE",
		PublicKey: make(ed25519.PublicKey, ed25519.PublicKeySize),
		ValidFrom: time.Now().UTC().Add(-time.Hour),
	}
	registry.keys["act_second"] = second
	if _, err := registry.RevokeKey(context.Background(), "tenant_act", "act_first",
		"security@example.test", time.Now().UTC()); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         "tenant_act",
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_third",
		SubjectPublicKey: hex.EncodeToString(public),
		SubjectHolder:    "treasury@example.test",
		Actor:            "risk@example.test",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            "nonce_third",
	}
	if err := auth.Sign(private, "act_first"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationRegistrarToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a revoked key authorized a new one: %d %s\n\nRevocation that does not "+
			"stop a key granting further authority is a record of an intention.",
			rec.Code, rec.Body.String())
	}
}

// A key cannot introduce itself: the signature would be self-referential, and anyone
// holding a private key could register it as an authorizer.
func TestAKeyCannotAuthorizeItsOwnRegistration(t *testing.T) {
	registry, _ := bootstrapped(t)
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	auth := policy.KeyAuthorization{
		SchemaVersion:    policy.KeyAuthorizationSchemaVersion,
		TenantID:         "tenant_act",
		Action:           policy.ActionRegisterKey,
		SubjectKeyID:     "act_self",
		SubjectPublicKey: hex.EncodeToString(public),
		SubjectHolder:    "whoever",
		Actor:            "whoever",
		AuthorizedAt:     time.Now().UTC(),
		Nonce:            "nonce_self",
	}
	if err := auth.Sign(private, "act_self"); err != nil {
		t.Fatalf("sign: %v", err)
	}

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys",
		map[string]any{"authorization": auth}, activationRegistrarToken))

	if rec.Code == http.StatusCreated {
		t.Fatal("a key authorized its own registration")
	}
}

// The agent-key registrar is a different, smaller privilege. So is the grant issuer.
func TestNeitherTheAgentKeyRegistrarNorTheIssuerCanRegisterActivationKeys(t *testing.T) {
	for _, token := range []string{keyRegistrarToken, agentToken} {
		registry := newActivationRegistry()
		handler := gateway.RegisterActivationKeyHandler(registry, nil,
			activationCredentials(t), nil, nil)

		body, _ := bootstrapBody(t, "act_escalation")
		rec := httptest.NewRecorder()
		handler(rec, activationRequest(t, "/v1/policy-activation-keys", body, token))

		if rec.Code != http.StatusForbidden {
			t.Fatalf("a credential that is not an activation-key registrar answered %d: "+
				"%s\n\nRegistering an agent key says which key is an agent; registering "+
				"an activation key says which key decides what every agent may not do. "+
				"An operator granted the first must not hold the second.",
				rec.Code, rec.Body.String())
		}
		if len(registry.keys) != 0 {
			t.Error("the key was stored anyway")
		}
	}
}

func TestAnUnauthenticatedCallerCannotRegisterAnActivationKey(t *testing.T) {
	registry := newActivationRegistry()
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	body, _ := bootstrapBody(t, "act_anon")
	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys", body, ""))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unauthenticated caller answered %d: %s", rec.Code, rec.Body.String())
	}
}

// A private key in the public key field is named, not reported as a length error: the
// caller has disclosed it and must generate another.
func TestAPrivateActivationKeyIsNamed(t *testing.T) {
	registry := newActivationRegistry()
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys", map[string]any{
		"key_id": "act_oops", "public_key": hex.EncodeToString(private),
		"holder": "risk@example.test", "actor": "ops@example.test",
	}, activationRegistrarToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a private key was accepted: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Errorf("the refusal does not tell the caller they have disclosed a private "+
			"key: %s", rec.Body.String())
	}
}

func TestAnUnknownFieldIsRefusedOnActivationKeys(t *testing.T) {
	registry := newActivationRegistry()
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	body, _ := bootstrapBody(t, "act_typo")
	// A misspelled authorization block would otherwise be dropped and the request read
	// as a bootstrap — which is exactly the escalation the signature exists to stop.
	body["authorisation"] = map[string]any{}

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys", body, activationRegistrarToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown field answered %d: %s", rec.Code, rec.Body.String())
	}
}

// The last active key is not revocable: a tenant with none can never authorize another
// policy change, including the rollback an incident needs.
func TestTheLastActivationKeyCannotBeRevoked(t *testing.T) {
	registry, _ := bootstrapped(t)
	handler := gateway.RevokeActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_first", "revoked_by": "security@example.test",
	}, activationRegistrarToken))

	if rec.Code != http.StatusConflict {
		t.Fatalf("the only activation key was revoked: %d %s", rec.Code, rec.Body.String())
	}
	if registry.keys["act_first"].Status != "ACTIVE" {
		t.Error("the key was revoked anyway")
	}
}

func TestARevocationNamesItsAuthor(t *testing.T) {
	registry, _ := bootstrapped(t)
	registry.keys["act_second"] = policy.ActivationKey{
		TenantID: "tenant_act", KeyID: "act_second", Status: "ACTIVE",
		ValidFrom: time.Now().UTC().Add(-time.Hour),
	}
	handler := gateway.RevokeActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_second",
	}, activationRegistrarToken))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unattributed revocation answered %d: %s", rec.Code, rec.Body.String())
	}

	ok := httptest.NewRecorder()
	handler(ok, activationRequest(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_second", "revoked_by": "security@example.test",
		"reason": "rotation",
	}, activationRegistrarToken))
	if ok.Code != http.StatusOK {
		t.Fatalf("revocation answered %d: %s", ok.Code, ok.Body.String())
	}
	if registry.keys["act_second"].RevokedBy != "security@example.test" {
		t.Errorf("revoked by %q", registry.keys["act_second"].RevokedBy)
	}
}

func TestAnAgentCredentialCannotRevokeAnActivationKey(t *testing.T) {
	registry, _ := bootstrapped(t)
	handler := gateway.RevokeActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys/revoke", map[string]any{
		"key_id": "act_first", "revoked_by": "whoever",
	}, agentToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a submission credential revoked policy authority: %d %s",
			rec.Code, rec.Body.String())
	}
}

// A store that failed is not reported as a registration. The caller would otherwise
// believe they hold policy authority they do not have.
func TestAFailedActivationStoreIsNotReportedAsSuccess(t *testing.T) {
	registry := newActivationRegistry()
	registry.fail = fmt.Errorf("the database is unreachable")
	handler := gateway.RegisterActivationKeyHandler(registry, nil, activationCredentials(t), nil, nil)

	body, _ := bootstrapBody(t, "act_unlucky")
	rec := httptest.NewRecorder()
	handler(rec, activationRequest(t, "/v1/policy-activation-keys", body, activationRegistrarToken))

	if rec.Code == http.StatusCreated {
		t.Fatalf("a failed store answered %d", rec.Code)
	}
}
