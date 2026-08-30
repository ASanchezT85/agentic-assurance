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
)

// Registering a signing key is the strongest thing this API does, and it is the newest.
//
// A grant says what an agent may do. A key registration says which key IS that agent, so
// whoever can perform one can act as any agent in the tenant — including agents holding
// grants they never issued and could not have widened. Everything below is about keeping
// that power narrow.

// keyRegistry is the store, in memory, recording what it was asked to do.
type keyRegistry struct {
	keys    map[string]identity.AgentKey
	revoked map[string]string
	events  []evidence.Event
	fail    error
}

func newKeyRegistry() *keyRegistry {
	return &keyRegistry{keys: map[string]identity.AgentKey{}, revoked: map[string]string{}}
}

func keyOf(tenant, agent, key string) string { return tenant + "|" + agent + "|" + key }

func (r *keyRegistry) Register(_ context.Context, k identity.AgentKey) (bool, error) {
	if r.fail != nil {
		return false, r.fail
	}
	id := keyOf(k.TenantID, k.AgentID, k.KeyID)
	if _, exists := r.keys[id]; exists {
		// The store refuses to overwrite; this mirrors that.
		return false, nil
	}
	r.keys[id] = k
	return true, nil
}

func (r *keyRegistry) RegisterWithEvidence(ctx context.Context, k identity.AgentKey,
	event evidence.Event) (bool, error) {

	registered, err := r.Register(ctx, k)
	if registered {
		r.events = append(r.events, event)
	}
	return registered, err
}

func (r *keyRegistry) Revoke(_ context.Context, tenant, agent, key, by string,
	_ time.Time) (bool, error) {

	if r.fail != nil {
		return false, r.fail
	}
	id := keyOf(tenant, agent, key)
	if _, exists := r.keys[id]; !exists {
		// The store refuses to report a revocation of a key it does not hold.
		return false, nil
	}
	if _, already := r.revoked[id]; already {
		return false, nil
	}
	r.revoked[id] = by
	return true, nil
}

func (r *keyRegistry) Exists(_ context.Context, tenant, agent, key string) (bool, error) {
	if r.fail != nil {
		return false, r.fail
	}
	_, exists := r.keys[keyOf(tenant, agent, key)]
	return exists, nil
}

const (
	registrarToken = "registrar-token-of-at-least-thirty-two-chars"
	agentToken     = "agent-token-of-at-least-thirty-two-characters"
)

// registrarCredentials is one tenant with two callers: one that may register keys and one
// that may only submit. The distinction is the subject of most of this file.
func registrarCredentials(t *testing.T) *identity.Credentials {
	t.Helper()
	creds, err := identity.ParseCredentials(fmt.Sprintf(
		"svc_registrar@tenant_keys=%s,svc_agent@tenant_keys=%s", registrarToken, agentToken))
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	creds.AllowKeyRegistrars("svc_registrar")
	return creds
}

func registerRequest(t *testing.T, body map[string]any, token string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-keys", strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func newPublicKey(t *testing.T) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return hex.EncodeToString(public)
}

func validKeyBody(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"agent_id":      "agent_alpha",
		"key_id":        "key_1",
		"algorithm":     identity.AlgorithmEd25519,
		"public_key":    newPublicKey(t),
		"registered_by": "ops@example.test",
	}
}

func TestARegistrarCanOnboardAnAgent(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, validKeyBody(t), registrarToken))

	if rec.Code != http.StatusCreated {
		t.Fatalf("registration answered %d: %s", rec.Code, rec.Body.String())
	}

	stored, ok := registry.keys[keyOf("tenant_keys", "agent_alpha", "key_1")]
	if !ok {
		t.Fatal("the key was accepted and not stored")
	}
	// The tenant came from the credential, never from the request.
	if stored.TenantID != "tenant_keys" {
		t.Errorf("stored under tenant %q", stored.TenantID)
	}
	if stored.Algorithm != identity.AlgorithmEd25519 {
		t.Errorf("stored algorithm %q", stored.Algorithm)
	}

	// The response identifies the key without being the key's only record of itself.
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response: %v", err)
	}
	if body["public_key_fingerprint"] == "" || body["public_key_fingerprint"] == nil {
		t.Error("the response does not let an operator check which key was registered " +
			"against the one their agent holds")
	}
}

// Submitting an intent and deciding which key may submit one are different powers.
func TestAnAgentCredentialCannotRegisterAKey(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, validKeyBody(t), agentToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("an ordinary credential registered a key: %d %s", rec.Code, rec.Body.String())
	}
	if len(registry.keys) != 0 {
		t.Error("a refused registration still reached the store")
	}
	if !strings.Contains(rec.Body.String(), "GATEWAY_KEY_REGISTRARS") {
		t.Error("the refusal does not say how the privilege is granted, so an operator " +
			"has to guess")
	}
}

// Issuing authority is not registering keys.
//
// The two are separated deliberately: an issuer who could also mint a key would reach
// every ceiling in the tenant through the back door, including grants issued by somebody
// else (P-002).
func TestAGrantIssuerCannotRegisterAKey(t *testing.T) {
	creds, err := identity.ParseCredentials("svc_issuer@tenant_keys=" + registrarToken)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	creds.AllowIssuers("svc_issuer")

	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, creds, nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, validKeyBody(t), registrarToken))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a grant issuer registered a signing key: %d. Issuing says what an agent "+
			"may do; registering says which key is that agent, and one credential holding "+
			"both can widen any ceiling in the tenant by minting a key for the agent that "+
			"already has one.", rec.Code)
	}
}

// An unauthenticated caller registers nothing.
func TestRegistrationRequiresACredential(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, validKeyBody(t), ""))

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("an unauthenticated request answered %d", rec.Code)
	}
	if len(registry.keys) != 0 {
		t.Error("an unauthenticated registration reached the store (INV-001)")
	}
}

// An existing key is never replaced.
//
// Overwriting would let one request take over an agent that already holds authority, and
// every envelope signed by the previous key would stop verifying at the same moment: a
// takeover and an outage sharing one code path.
func TestAnExistingKeyIsNotOverwritten(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	first := validKeyBody(t)
	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, first, registrarToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("the first registration answered %d", rec.Code)
	}
	original := registry.keys[keyOf("tenant_keys", "agent_alpha", "key_1")].PublicKey

	// Same agent, same key id, a different public key: the takeover.
	takeover := validKeyBody(t)
	takeover["registered_by"] = "attacker@example.test"
	rec = httptest.NewRecorder()
	handler(rec, registerRequest(t, takeover, registrarToken))

	if rec.Code != http.StatusConflict {
		t.Errorf("re-registering an existing key id answered %d; it must be refused, and "+
			"the caller must be told, because a caller who reads \"no error\" believes "+
			"their key is live while the one in force is somebody else's", rec.Code)
	}
	if got := registry.keys[keyOf("tenant_keys", "agent_alpha", "key_1")].PublicKey; string(got) != string(original) {
		t.Error("the stored key was replaced")
	}
	if !strings.Contains(rec.Body.String(), "revoke") {
		t.Error("the refusal does not name rotation as the way through, so an operator " +
			"whose key is compromised is left with no next step")
	}
}

// A private key sent by mistake is named as one.
func TestAPrivateKeyIsRefusedAndNamed(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	body := validKeyBody(t)
	body["public_key"] = hex.EncodeToString(private)

	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, body, registrarToken))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a private key answered %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "PRIVATE") {
		t.Errorf("the refusal does not tell the caller they have just disclosed a private "+
			"key: %s", rec.Body.String())
	}
	if len(registry.keys) != 0 {
		t.Error("a private key reached the store")
	}
}

// Everything a key needs to be usable and attributable is required.
func TestAnUnusableKeyIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		expect string
	}{
		{"no agent", func(b map[string]any) { delete(b, "agent_id") }, "agent_id"},
		{"no key id", func(b map[string]any) { delete(b, "key_id") }, "key_id"},
		{"no registrar", func(b map[string]any) { delete(b, "registered_by") }, "registered_by"},
		{"no key", func(b map[string]any) { delete(b, "public_key") }, "public_key"},
		{"not hex", func(b map[string]any) { b["public_key"] = "zzzz" }, "hex"},
		{"wrong size", func(b map[string]any) { b["public_key"] = "aabbcc" }, "bytes"},
		{"another algorithm", func(b map[string]any) { b["algorithm"] = "RSA" }, "not supported"},
		{"empty window", func(b map[string]any) {
			at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
			b["valid_from"] = at
			b["valid_until"] = at
		}, "valid_until"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := validKeyBody(t)
			c.mutate(body)

			registry := newKeyRegistry()
			handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

			rec := httptest.NewRecorder()
			handler(rec, registerRequest(t, body, registrarToken))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("answered %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), c.expect) {
				t.Errorf("the refusal does not mention %q: %s", c.expect, rec.Body.String())
			}
			if len(registry.keys) != 0 {
				t.Error("an unusable key reached the store")
			}
		})
	}
}

// A field the platform does not know is refused rather than dropped.
//
// A misspelled validity window would otherwise be silently ignored and the key
// registered without the expiry its author wrote down.
func TestAnUnknownFieldIsRefused(t *testing.T) {
	body := validKeyBody(t)
	body["valid_untill"] = "2026-12-31T00:00:00Z"

	registry := newKeyRegistry()
	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, body, registrarToken))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("a misspelled field was accepted (%d); the key would be registered "+
			"without the expiry its author wrote down", rec.Code)
	}
}

// A registration that could not be recorded is not reported as one.
func TestAFailedRegistrationIsNotReportedAsSuccess(t *testing.T) {
	registry := newKeyRegistry()
	registry.fail = constError("the key store is unavailable")

	handler := gateway.RegisterAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)
	rec := httptest.NewRecorder()
	handler(rec, registerRequest(t, validKeyBody(t), registrarToken))

	if rec.Code == http.StatusCreated {
		t.Fatal("a registration that failed to store answered 201; an operator would " +
			"deploy an agent whose envelopes cannot verify")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("answered %d, want 503", rec.Code)
	}
}

// Revocation exists, and is the other half of rotation.
//
// Without it the first key compromise still needs database access, which is the gap this
// endpoint was built to close.
func TestAKeyCanBeRevokedByARegistrar(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RevokeAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	// A key to revoke. Revoking one that was never registered is a different answer now,
	// and a test that skipped the registration was asserting on that path by accident.
	registry.keys[keyOf("tenant_keys", "agent_alpha", "key_1")] = identity.AgentKey{
		TenantID: "tenant_keys", AgentID: "agent_alpha", KeyID: "key_1", Status: "ACTIVE",
	}

	body, _ := json.Marshal(map[string]any{
		"agent_id": "agent_alpha", "key_id": "key_1",
		"revoked_by": "security@example.test", "reason": "the holder left",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-keys/revoke", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+registrarToken)

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("revocation answered %d: %s", rec.Code, rec.Body.String())
	}
	if by := registry.revoked[keyOf("tenant_keys", "agent_alpha", "key_1")]; by != "security@example.test" {
		t.Errorf("the revocation recorded %q as its author", by)
	}
}

// A revocation with no author is refused.
func TestARevocationNamesWhoDidIt(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RevokeAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	body, _ := json.Marshal(map[string]any{"agent_id": "agent_alpha", "key_id": "key_1"})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-keys/revoke", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+registrarToken)

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("a revocation with no author answered %d; a key that stopped being "+
			"trusted for no recorded reason is an operational mystery six months later",
			rec.Code)
	}
	if len(registry.revoked) != 0 {
		t.Error("an incomplete revocation reached the store")
	}
}

// An ordinary credential cannot revoke either. Revoking a key is stopping an agent, and
// an agent that can stop another agent is a fleet control nobody authorized.
func TestAnAgentCredentialCannotRevokeAKey(t *testing.T) {
	registry := newKeyRegistry()
	handler := gateway.RevokeAgentKeyHandler(registry, nil, registrarCredentials(t), nil, nil)

	body, _ := json.Marshal(map[string]any{
		"agent_id": "agent_alpha", "key_id": "key_1", "revoked_by": "somebody",
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/agent-keys/revoke", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+agentToken)

	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("an ordinary credential revoked a key: %d", rec.Code)
	}
	if len(registry.revoked) != 0 {
		t.Error("a refused revocation reached the store")
	}
}

type constError string

func (e constError) Error() string { return string(e) }
