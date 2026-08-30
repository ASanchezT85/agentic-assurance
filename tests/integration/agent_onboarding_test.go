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
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
)

// Onboarding an agent, over the API, end to end.
//
// The unit tests hold the handler to its security properties. This one asks the question
// a customer asks: can I add an agent to my own platform and have it trade, without
// anyone touching the database?
//
// Until this endpoint existed the answer was no. Envelope signatures became mandatory in
// the second remediation and nothing registered a signing key, so every onboarding needed
// a psql session — which is not a product, and which pushed operators toward a credential
// far stronger than the task.

func TestAnAgentCanBeOnboardedOverTheAPI(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)
	ctx := context.Background()

	agentID := fmt.Sprintf("agent_onboard_%d", now.UnixNano())
	keyID := "key_onboard"

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	// 1. The customer registers the key. No database access.
	status, body := rig.postJSON(t, "/v1/agent-keys", map[string]any{
		"agent_id":      agentID,
		"key_id":        keyID,
		"algorithm":     identity.AlgorithmEd25519,
		"public_key":    hex.EncodeToString(public),
		"registered_by": "ops@example.test",
	}, rig.registrarToken)
	if status != http.StatusCreated {
		t.Fatalf("registration answered %d: %v", status, body)
	}
	t.Logf("registered %s/%s, fingerprint %v", agentID, keyID, body["public_key_fingerprint"])

	// 2. The agent signs an envelope with the matching private key, and the platform
	//    verifies it against what was just registered. This is the whole point: the key
	//    the customer sent over HTTP is the key the hot path checks.
	grantID := rig.grantForAgent(t, agentID, now)
	key := fmt.Sprintf("onboard-%d", now.UnixNano())
	envelope := rig.envelopeSignedBy(t, now, key, agentID, keyID, grantID, private)

	status, decoded := rig.post(t, envelope, true)
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("an envelope signed by the freshly registered key was refused with %d: %v",
			status, decoded)
	}

	// 3. And the registration is in the record. Binding a key to an agent is the moment
	//    a public key becomes able to act, and an evidence chain that cannot say who did
	//    that ends one step short of the question an investigation asks.
	chain, err := rig.evidence.ByAggregate(ctx, rig.tenant, agentID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	var registered *evidence.Event
	for i, e := range chain {
		if e.EventName == evidence.AgentKeyRegistered {
			registered = &chain[i]
		}
	}
	if registered == nil {
		t.Fatalf("no %s in the record; the set of keys able to act as an agent changed "+
			"and nothing wrote it down", evidence.AgentKeyRegistered)
	}
	if registered.Payload["registered_by"] != "ops@example.test" {
		t.Errorf("the registration names %v as its author", registered.Payload["registered_by"])
	}
	if registered.Payload["public_key_fingerprint"] == nil {
		t.Error("the record does not say which key was registered")
	}

	// 4. Revoked, the same envelope stops verifying. Rotation and compromise both end
	//    here, and without it the first incident would still need database access.
	status, body = rig.postJSON(t, "/v1/agent-keys/revoke", map[string]any{
		"agent_id": agentID, "key_id": keyID,
		"revoked_by": "security@example.test", "reason": "onboarding test",
	}, rig.registrarToken)
	if status != http.StatusOK {
		t.Fatalf("revocation answered %d: %v", status, body)
	}

	after := rig.envelopeSignedBy(t, now, key+"-after", agentID, keyID, grantID, private)
	status, decoded = rig.post(t, after, true)
	if status == http.StatusOK || status == http.StatusAccepted {
		t.Errorf("an envelope signed by a revoked key was accepted (%d): a revocation "+
			"that does not stop signatures is a record of an intention", status)
	}
	t.Logf("after revocation: %d %v", status, decoded["code"])
}

// An agent credential cannot onboard an agent, against the running gateway.
func TestOnboardingRequiresTheRegistrarPrivilegeOverHTTP(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}

	status, body := rig.postJSON(t, "/v1/agent-keys", map[string]any{
		"agent_id":      "agent_intruder",
		"key_id":        "key_1",
		"public_key":    hex.EncodeToString(public),
		"registered_by": "whoever",
	}, e2eToken)

	if status != http.StatusForbidden {
		t.Fatalf("the submission credential registered a key over HTTP: %d %v", status, body)
	}
}

// --- rig helpers ----------------------------------------------------------------

// postJSON sends a JSON body to one of the gateway's administrative endpoints.
func (r *e2eRig) postJSON(t *testing.T, path string, body map[string]any,
	token string) (int, map[string]any) {

	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, r.server.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(payload, &decoded)
	return resp.StatusCode, decoded
}
