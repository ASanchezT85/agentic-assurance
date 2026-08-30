package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
)

// Registering the key that makes an agent an agent.
//
// Until this existed, onboarding an agent meant writing a row into agent_signing_keys by
// hand. Envelope signatures became mandatory in the second remediation, so a customer
// could not add an agent to their own platform without database access — which is not a
// product, and which pushed every operator toward a credential far stronger than the task
// needed.
//
// It is a separate privilege from issuing authority, and that separation is the whole
// design. A grant says "this agent may trade up to X". A key registration says "this
// public key IS that agent" — so whoever holds it can mint a key for any agent in the
// tenant, including one whose grant they never issued and could not have widened. An
// issuer who could also register keys would reach every ceiling in the tenant through the
// back door, which is exactly the shape P-002 forbids.
//
// What it will not do:
//
//   - accept a tenant from the request. The tenant comes from the credential, as it does
//     for grants, because letting the body name whose agent this is would be the
//     cross-tenant hole in its most direct form (INV-007);
//   - replace an existing key. Rotation is a new key id plus a revocation of the old one.
//     Overwriting would let one request take over an agent that already holds authority,
//     and every envelope signed by the previous key would stop verifying at the same
//     moment — an outage and a takeover sharing one code path;
//   - accept anything but ed25519. The algorithm is the platform's decision, not the
//     caller's: a negotiated set is a downgrade attack with extra steps.

// KeyRegistry is what this handler needs from the key store.
type KeyRegistry interface {
	// Register writes the key and its evidence in one transaction. Binding a public key
	// to an agent changes which key may act as that agent, and a registration that
	// committed while its evidence write failed would leave the platform trusting a key
	// nothing in the record accounts for (F4-K006).
	Register(ctx context.Context, k identity.AgentKey) (bool, error)
	RegisterWithEvidence(ctx context.Context, k identity.AgentKey, event evidence.Event) (bool, error)

	// Revoke reports whether it revoked anything, so an unknown key is not answered as a
	// successful revocation and does not leave an evidence event saying one happened.
	Revoke(ctx context.Context, tenantID, agentID, keyID, revokedBy string, at time.Time) (bool, error)

	// Exists separates "already revoked" from "no such key".
	Exists(ctx context.Context, tenantID, agentID, keyID string) (bool, error)
}

// agentKeyRequest is what a customer sends to register a signing key.
//
// The tenant is deliberately absent, for the reason above.
type agentKeyRequest struct {
	AgentID string `json:"agent_id"`
	KeyID   string `json:"key_id"`

	// Algorithm is accepted so a caller states what they think they are sending and is
	// told plainly when the platform disagrees. It is validated, never honoured.
	Algorithm string `json:"algorithm"`

	// PublicKey is hex-encoded. Public by definition; a private key is never accepted,
	// and a caller who sends one has made a mistake this platform must not absorb
	// silently — 64 hex characters is a public key and 128 is a private one, so the
	// length check names it.
	PublicKey string `json:"public_key"`

	RegisteredBy string `json:"registered_by"`

	ValidFrom  *time.Time `json:"valid_from,omitempty"`
	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

func (k agentKeyRequest) validate() (ed25519.PublicKey, []string) {
	var problems []string

	if strings.TrimSpace(k.AgentID) == "" {
		problems = append(problems, "agent_id is required")
	}
	if strings.TrimSpace(k.KeyID) == "" {
		problems = append(problems, "key_id is required; a signature names the key that "+
			"produced it, and an agent with one unnamed key can never be rotated")
	}
	if strings.TrimSpace(k.RegisteredBy) == "" {
		problems = append(problems, "registered_by is required; binding a key to an "+
			"agent is an act that must be attributable months later")
	}
	if k.Algorithm != "" && k.Algorithm != identity.AlgorithmEd25519 {
		problems = append(problems, fmt.Sprintf(
			"algorithm %q is not supported; this platform accepts %s only, because an "+
				"algorithm the caller chooses is a downgrade the caller can perform",
			k.Algorithm, identity.AlgorithmEd25519))
	}

	decoded, err := hex.DecodeString(strings.TrimSpace(k.PublicKey))
	switch {
	case strings.TrimSpace(k.PublicKey) == "":
		problems = append(problems, "public_key is required")
	case err != nil:
		problems = append(problems, "public_key is not hex")
	case len(decoded) == ed25519.PrivateKeySize:
		// Named rather than reported as a length error. A caller who pasted a private
		// key needs to know they have disclosed it and must generate another.
		problems = append(problems, "public_key is 64 bytes, which is an ed25519 PRIVATE "+
			"key. It has now been sent over the network: generate a new pair and register "+
			"the 32-byte public half")
	case len(decoded) != ed25519.PublicKeySize:
		problems = append(problems, fmt.Sprintf(
			"public_key is %d bytes; an ed25519 public key is %d",
			len(decoded), ed25519.PublicKeySize))
	}

	if k.ValidFrom != nil && k.ValidUntil != nil && !k.ValidUntil.After(*k.ValidFrom) {
		problems = append(problems, "valid_until must be after valid_from; a key whose "+
			"window is empty verifies nothing and would look registered")
	}

	if len(problems) > 0 {
		return nil, problems
	}
	return ed25519.PublicKey(decoded), nil
}

// RegisterAgentKeyHandler is POST /v1/agent-keys.
func RegisterAgentKeyHandler(keys KeyRegistry, evidenceStore *evidence.Store,
	creds *identity.Credentials, verifier *identity.Verifier, now func() time.Time) http.HandlerFunc {

	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, caller, ok := registrar(w, r, creds, verifier)
		if !ok {
			return
		}
		if keys == nil {
			writeJSON(w, http.StatusServiceUnavailable,
				errorBody("no key store is configured"))
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil || len(raw) > MaxEnvelopeBytes {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}

		var req agentKeyRequest
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		// Unknown fields are refused. A misspelled validity window would otherwise be
		// dropped and the key registered without the expiry its author wrote down.
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("the request is not an agent key: "+err.Error()))
			return
		}

		public, problems := req.validate()
		if len(problems) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "the key would not be usable",
				"details": problems,
			})
			return
		}

		at := now().UTC()
		validFrom := at
		if req.ValidFrom != nil {
			validFrom = req.ValidFrom.UTC()
		}
		key := identity.AgentKey{
			TenantID:  tenant,
			AgentID:   strings.TrimSpace(req.AgentID),
			KeyID:     strings.TrimSpace(req.KeyID),
			Algorithm: identity.AlgorithmEd25519,
			PublicKey: public,
			Status:    "ACTIVE",
			ValidFrom: validFrom,
		}
		if req.ValidUntil != nil {
			key.ValidUntil = req.ValidUntil.UTC()
		}

		event := agentKeyEvent(evidence.AgentKeyRegistered, key, at, map[string]any{
			"registered_by": strings.TrimSpace(req.RegisteredBy),
			"authorized_by": caller,
			// The public key's fingerprint rather than the key. It is public and could
			// be echoed whole; a fingerprint is what an operator compares against what
			// their agent holds, and it fits in a log line.
			"public_key_fingerprint": fingerprint(public),
			"valid_from":             key.ValidFrom,
		})

		registered, err := keys.RegisterWithEvidence(r.Context(), key, event)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				errorBody("the key could not be recorded, so it was not registered"))
			return
		}
		if !registered {
			// The security property, surfaced. A caller told "no error" would believe
			// their key is live while the one in force is somebody else's.
			writeJSON(w, http.StatusConflict, errorBody(fmt.Sprintf(
				"agent %s already has a key %s and it was not replaced. A key is not "+
					"overwritten: doing so would take over an agent that already holds "+
					"authority, and every envelope signed by the previous key would stop "+
					"verifying at the same moment. Register a new key id, then revoke "+
					"this one.", key.AgentID, key.KeyID)))
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"agent_id":               key.AgentID,
			"key_id":                 key.KeyID,
			"algorithm":              key.Algorithm,
			"public_key_fingerprint": fingerprint(public),
			"valid_from":             key.ValidFrom,
			"registered_at":          at,
		})
	}
}

// revokeKeyRequest names the key to stop trusting, and why.
type revokeKeyRequest struct {
	AgentID string `json:"agent_id"`
	KeyID   string `json:"key_id"`
	// RevokedBy is required for the same reason a revocation date is kept: a key that
	// stopped being trusted for no recorded reason is an operational mystery later.
	RevokedBy string `json:"revoked_by"`
	Reason    string `json:"reason,omitempty"`
}

// RevokeAgentKeyHandler is POST /v1/agent-keys/revoke.
//
// Registration without revocation would be half an onboarding story: the first key
// compromise would still need database access, which is the gap this endpoint exists to
// close. A revoked key is kept rather than deleted — an envelope signed last week was
// signed by a key that was valid last week, and an evidence chain referencing a row
// nobody can find is unreadable exactly when it matters.
func RevokeAgentKeyHandler(keys KeyRegistry, evidenceStore *evidence.Store,
	creds *identity.Credentials, verifier *identity.Verifier, now func() time.Time) http.HandlerFunc {

	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, caller, ok := registrar(w, r, creds, verifier)
		if !ok {
			return
		}
		if keys == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no key store is configured"))
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil || len(raw) > MaxEnvelopeBytes {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}

		var req revokeKeyRequest
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("the request is not a key revocation: "+err.Error()))
			return
		}

		var problems []string
		if strings.TrimSpace(req.AgentID) == "" {
			problems = append(problems, "agent_id is required")
		}
		if strings.TrimSpace(req.KeyID) == "" {
			problems = append(problems, "key_id is required")
		}
		if strings.TrimSpace(req.RevokedBy) == "" {
			problems = append(problems, "revoked_by is required; a key that stopped being "+
				"trusted for no recorded reason is an operational mystery six months later")
		}
		if len(problems) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "the revocation is incomplete", "details": problems,
			})
			return
		}

		at := now().UTC()
		agentID := strings.TrimSpace(req.AgentID)
		keyID := strings.TrimSpace(req.KeyID)

		revoked, err := keys.Revoke(r.Context(), tenant, agentID, keyID,
			strings.TrimSpace(req.RevokedBy), at)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable,
				errorBody("the revocation could not be recorded, so the key is still trusted"))
			return
		}
		if !revoked {
			// Nothing changed. Which of the two it is matters: an already-revoked key is
			// an idempotent repeat, and an unknown one is very likely a typo in an agent
			// id at the moment somebody believes they have contained a compromise.
			exists, existsErr := keys.Exists(r.Context(), tenant, agentID, keyID)
			if existsErr != nil {
				writeJSON(w, http.StatusServiceUnavailable,
					errorBody("the key's state could not be read"))
				return
			}
			if !exists {
				writeJSON(w, http.StatusNotFound, errorBody(fmt.Sprintf(
					"no signing key %s is registered for agent %s in this tenant. "+
						"Nothing was revoked: check the agent id, because a key you "+
						"believe you have just stopped is still trusted",
					keyID, agentID)))
				return
			}
			// Already revoked. Answered as the no-op it is, and deliberately without a
			// second revocation event: an audit trail that records a transition which
			// did not happen is worse than one entry short.
			writeJSON(w, http.StatusOK, map[string]any{
				"agent_id": agentID, "key_id": keyID, "revoked": false,
				"note": "this key was already revoked; nothing changed",
			})
			return
		}

		recordAgentKeyEvent(r.Context(), evidenceStore, evidence.AgentKeyRevoked,
			identity.AgentKey{TenantID: tenant, AgentID: agentID, KeyID: keyID}, at,
			map[string]any{
				"revoked_by":    strings.TrimSpace(req.RevokedBy),
				"authorized_by": caller,
				"reason":        strings.TrimSpace(req.Reason),
			})

		writeJSON(w, http.StatusOK, map[string]any{
			"agent_id": agentID, "key_id": keyID, "revoked": true, "revoked_at": at,
		})
	}
}

// registrar authenticates the caller and checks the key-registrar privilege.
func registrar(w http.ResponseWriter, r *http.Request, creds *identity.Credentials,
	verifier *identity.Verifier) (tenant, caller string, ok bool) {

	tenant, status, message := callerTenant(r, creds, verifier)
	if tenant == "" {
		writeJSON(w, status, errorBody(message))
		return "", "", false
	}

	if verifier == nil {
		verifier = &identity.Verifier{}
	}
	attested := verifier.Resolve(presentedFrom(r, creds))
	if !attested.MayRegisterKeys {
		writeJSON(w, http.StatusForbidden, errorBody(
			"this credential may not register signing keys. Registering a key says which "+
				"key is an agent, so whoever holds it can act as any agent in the tenant; "+
				"it is separated from both submitting and issuing authority. Name the "+
				"registrar in GATEWAY_KEY_REGISTRARS"))
		return "", "", false
	}
	return tenant, attested.APIIdentity, true
}

// recordAgentKeyEvent writes down that a key was revoked.
//
// Best effort, and only for revocation. Containment must not wait on a secondary store: a
// key believed compromised has to stop verifying whether or not the evidence write
// succeeds. Registration is the other direction — it grants the ability to act, so it
// shares a transaction with its evidence and neither happens without the other.
func recordAgentKeyEvent(ctx context.Context, store *evidence.Store, name evidence.EventName,
	key identity.AgentKey, at time.Time, payload map[string]any) {

	if store == nil {
		return
	}
	_, _ = store.Append(ctx, agentKeyEvent(name, key, at, payload))
}

// agentKeyEvent builds the record that the set of keys able to act as an agent changed.
func agentKeyEvent(name evidence.EventName, key identity.AgentKey, at time.Time,
	payload map[string]any) evidence.Event {

	payload["agent_id"] = key.AgentID
	payload["key_id"] = key.KeyID

	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_%s_%s_%d", key.AgentID, key.KeyID, name, at.UnixNano()),
		EventName:     name,
		TenantID:      key.TenantID,
		AggregateID:   key.AgentID,
		CorrelationID: key.AgentID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload:       payload,
	}
}

// fingerprint is the first eight bytes of the public key, hex.
//
// Enough for a human to compare what the platform holds against what their agent holds,
// and short enough to read out loud. The key itself is public and is stored whole; this
// is for the places a whole key would be noise.
func fingerprint(public ed25519.PublicKey) string {
	if len(public) < 8 {
		return ""
	}
	return hex.EncodeToString(public[:8])
}
