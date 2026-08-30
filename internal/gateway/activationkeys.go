package gateway

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/policy"
)

// Registering the key that may decide which policy enforces.
//
// This is the strongest act the API exposes, and it is the last one that still required
// database access. An activation key authorizes a bundle into force; a bundle says what
// every agent in the tenant may not do — so whoever can add an activation key can hand
// themselves the power to lift every ceiling the customer set, without touching a single
// grant or agent key.
//
// So it is not the agent-key endpoint with a different table. Two rules shape it:
//
//   - Once a tenant holds an activation key, a further key is added only by an
//     authorization signed by an existing one. The customer's authority extends itself;
//     the platform's operator never mints policy authority for a customer who already has
//     some. That is INV-009 at the key layer — the platform does not sign what constrains
//     it, and this endpoint is a place it would have been very easy to let it.
//
//   - The first key of a tenant is the exception, because a tenant with no key cannot
//     sign anything. It is a bootstrap: gated by a named operator credential, recorded as
//     a bootstrap rather than as an authorized registration, and possible exactly once.

// ActivationKeyRegistry is what these handlers need from the activation store.
type ActivationKeyRegistry interface {
	ActiveKeys(ctx context.Context, tenantID string) (int, error)
	Key(ctx context.Context, tenantID, keyID string) (*policy.ActivationKey, error)
	RegisterKeyAuthorized(ctx context.Context, k policy.ActivationKey,
		nonce, action, actor, signedBy string, authorizedAt time.Time,
		event evidence.Event, at time.Time) (bool, error)
	RevokeKey(ctx context.Context, tenantID, keyID, by string, at time.Time) (bool, error)
}

// activationKeyRequest is either a bootstrap or a signed authorization.
//
// The bootstrap fields are the ones a first key needs and nothing more. The authorization
// is the customer's signed document, passed through whole: re-deriving it from loose
// fields would mean the platform assembling what it then verifies, and a signature over a
// document the verifier built is a signature over the verifier's opinion.
type activationKeyRequest struct {
	// Bootstrap, for a tenant with no activation key yet.
	KeyID     string `json:"key_id,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Holder    string `json:"holder,omitempty"`
	Actor     string `json:"actor,omitempty"`
	Reason    string `json:"reason,omitempty"`

	// Authorization, for every key after the first.
	Authorization *policy.KeyAuthorization `json:"authorization,omitempty"`

	ValidUntil *time.Time `json:"valid_until,omitempty"`
}

// RegisterActivationKeyHandler is POST /v1/policy-activation-keys.
func RegisterActivationKeyHandler(keys ActivationKeyRegistry, evidenceStore *evidence.Store,
	creds *identity.Credentials, verifier *identity.Verifier, now func() time.Time) http.HandlerFunc {

	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, caller, ok := activationRegistrar(w, r, creds, verifier)
		if !ok {
			return
		}
		if keys == nil {
			writeJSON(w, http.StatusServiceUnavailable,
				errorBody("no activation key store is configured"))
			return
		}

		var req activationKeyRequest
		if !decodeActivationKeyBody(w, r, &req) {
			return
		}

		existing, err := keys.ActiveKeys(r.Context(), tenant)
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody(
				"the tenant's activation keys could not be read, so it is not known "+
					"whether this would be the first"))
			return
		}

		at := now().UTC()
		var key policy.ActivationKey
		var nonce, action, actor, signedBy string
		var authorizedAt time.Time

		if existing == 0 {
			// The bootstrap. Once per tenant, and only while nothing could have signed.
			if req.Authorization != nil {
				writeJSON(w, http.StatusBadRequest, errorBody(
					"this tenant has no activation key, so there is no key that could "+
						"have signed this authorization. Send the bootstrap form: "+
						"key_id, public_key, holder and actor"))
				return
			}
			public, problems := bootstrapKey(req)
			if len(problems) > 0 {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "the key would not be usable", "details": problems,
				})
				return
			}
			key = policy.ActivationKey{
				TenantID: tenant, KeyID: strings.TrimSpace(req.KeyID),
				Algorithm: policy.AlgorithmEd25519, PublicKey: public,
				Holder: strings.TrimSpace(req.Holder), Status: "ACTIVE", ValidFrom: at,
			}
			nonce = "bootstrap_" + key.KeyID
			action = "BOOTSTRAP_ACTIVATION_KEY"
			actor = strings.TrimSpace(req.Actor)
			authorizedAt = at
		} else {
			// Every later key: signed for by one already registered.
			if req.Authorization == nil {
				writeJSON(w, http.StatusBadRequest, errorBody(fmt.Sprintf(
					"this tenant already holds %d active activation key(s), so a further "+
						"key must be authorized by one of them. An operator credential "+
						"does not grant policy authority to a customer who already has "+
						"some: send a signed authorization", existing)))
				return
			}
			auth := *req.Authorization
			if auth.TenantID != tenant {
				// Named rather than folded into a signature failure: the authorization
				// may be perfectly valid and simply belong to somebody else (INV-007).
				writeJSON(w, http.StatusForbidden, errorBody(
					"the authorization names a different tenant than the credential"))
				return
			}
			if err := auth.Validate(); err != nil {
				writeActivationKeyError(w, err)
				return
			}
			signer, err := keys.Key(r.Context(), tenant, auth.Signature.KeyID)
			if err != nil {
				writeActivationKeyError(w, err)
				return
			}
			if err := signer.Usable(at); err != nil {
				writeActivationKeyError(w, err)
				return
			}
			if err := auth.Verify(signer.PublicKey); err != nil {
				writeActivationKeyError(w, err)
				return
			}
			public, err := auth.PublicKey()
			if err != nil {
				writeActivationKeyError(w, err)
				return
			}
			key = policy.ActivationKey{
				TenantID: tenant, KeyID: strings.TrimSpace(auth.SubjectKeyID),
				Algorithm: policy.AlgorithmEd25519, PublicKey: public,
				Holder: strings.TrimSpace(auth.SubjectHolder), Status: "ACTIVE",
				ValidFrom: at,
			}
			nonce = auth.Nonce
			action = string(auth.Action)
			actor = auth.Actor
			signedBy = auth.Signature.KeyID
			authorizedAt = auth.AuthorizedAt.UTC()
		}
		if req.ValidUntil != nil {
			until := req.ValidUntil.UTC()
			key.ValidUntil = &until
		}

		event := activationKeyEvent(evidence.ActivationKeyRegistered, key, at, map[string]any{
			"holder":                 key.Holder,
			"actor":                  actor,
			"authorized_by_key_id":   signedBy,
			"bootstrap":              signedBy == "",
			"api_identity":           caller,
			"public_key_fingerprint": hex.EncodeToString(key.PublicKey[:8]),
		})

		registered, err := keys.RegisterKeyAuthorized(r.Context(), key, nonce, action,
			actor, signedBy, authorizedAt, event, at)
		switch {
		case errors.Is(err, policy.ErrReplayed):
			writeJSON(w, http.StatusConflict, errorBody(
				"this authorization has already been accepted. A captured authorization "+
					"presented a second time is refused: registering a key that decides "+
					"which policy enforces happens once per authorization"))
			return
		case err != nil:
			writeJSON(w, http.StatusServiceUnavailable, errorBody(
				"the key could not be recorded, so it was not registered"))
			return
		case !registered:
			writeJSON(w, http.StatusConflict, errorBody(fmt.Sprintf(
				"activation key %s already exists and was not replaced. Overwriting it "+
					"would substitute the authority that decides which policy enforces. "+
					"Register a new key id, then revoke this one.", key.KeyID)))
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"key_id":                 key.KeyID,
			"algorithm":              key.Algorithm,
			"holder":                 key.Holder,
			"public_key_fingerprint": hex.EncodeToString(key.PublicKey[:8]),
			"bootstrap":              signedBy == "",
			"authorized_by_key_id":   signedBy,
			"registered_at":          at,
		})
	}
}

// revokeActivationKeyRequest names the key to stop trusting, and why.
type revokeActivationKeyRequest struct {
	KeyID     string `json:"key_id"`
	RevokedBy string `json:"revoked_by"`
	Reason    string `json:"reason,omitempty"`
}

// RevokeActivationKeyHandler is POST /v1/policy-activation-keys/revoke.
//
// Not signed for, deliberately. The case that matters is a key believed compromised, and
// requiring that key's cooperation to retire it would be requiring the attacker's. The
// store refuses to revoke the last active key, so this cannot leave a tenant unable to
// authorize the rollback an incident needs.
func RevokeActivationKeyHandler(keys ActivationKeyRegistry, evidenceStore *evidence.Store,
	creds *identity.Credentials, verifier *identity.Verifier, now func() time.Time) http.HandlerFunc {

	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, caller, ok := activationRegistrar(w, r, creds, verifier)
		if !ok {
			return
		}
		if keys == nil {
			writeJSON(w, http.StatusServiceUnavailable,
				errorBody("no activation key store is configured"))
			return
		}

		var req revokeActivationKeyRequest
		if !decodeActivationKeyBody(w, r, &req) {
			return
		}

		var problems []string
		if strings.TrimSpace(req.KeyID) == "" {
			problems = append(problems, "key_id is required")
		}
		if strings.TrimSpace(req.RevokedBy) == "" {
			problems = append(problems, "revoked_by is required; a key that stopped "+
				"being trusted for no recorded reason is an operational mystery later")
		}
		if len(problems) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "the revocation is incomplete", "details": problems,
			})
			return
		}

		at := now().UTC()
		keyID := strings.TrimSpace(req.KeyID)

		revoked, err := keys.RevokeKey(r.Context(), tenant, keyID,
			strings.TrimSpace(req.RevokedBy), at)
		var refusal *policy.ActivationError
		switch {
		case errors.As(err, &refusal):
			writeJSON(w, http.StatusConflict, errorBody(refusal.Message))
			return
		case err != nil:
			writeJSON(w, http.StatusServiceUnavailable, errorBody(
				"the revocation could not be recorded, so the key is still trusted"))
			return
		case !revoked:
			writeJSON(w, http.StatusNotFound, errorBody(fmt.Sprintf(
				"no active activation key %s is registered for this tenant", keyID)))
			return
		}

		recordActivationKeyEvent(r.Context(), evidenceStore, evidence.ActivationKeyRevoked,
			policy.ActivationKey{TenantID: tenant, KeyID: keyID}, at, map[string]any{
				"revoked_by":   strings.TrimSpace(req.RevokedBy),
				"api_identity": caller,
				"reason":       strings.TrimSpace(req.Reason),
			})

		writeJSON(w, http.StatusOK, map[string]any{"key_id": keyID, "revoked_at": at})
	}
}

// bootstrapKey validates the first-key form.
func bootstrapKey(req activationKeyRequest) ([]byte, []string) {
	var problems []string
	if strings.TrimSpace(req.KeyID) == "" {
		problems = append(problems, "key_id is required")
	}
	if strings.TrimSpace(req.Holder) == "" {
		problems = append(problems, "holder is required; a key that authorized a policy "+
			"change months ago must still be attributable to someone")
	}
	if strings.TrimSpace(req.Actor) == "" {
		problems = append(problems, "actor is required; bootstrapping the authority that "+
			"decides what the platform denies must name who did it")
	}

	decoded, err := hex.DecodeString(strings.TrimSpace(req.PublicKey))
	switch {
	case strings.TrimSpace(req.PublicKey) == "":
		problems = append(problems, "public_key is required")
	case err != nil:
		problems = append(problems, "public_key is not hex")
	case len(decoded) == 64:
		// Named rather than reported as a length error. A caller who pasted a private
		// key needs to know they have disclosed it and must generate another.
		problems = append(problems, "public_key is 64 bytes, which is an ed25519 PRIVATE "+
			"key. It has now been sent over the network: generate a new pair and register "+
			"the 32-byte public half")
	case len(decoded) != 32:
		problems = append(problems, fmt.Sprintf(
			"public_key is %d bytes; an ed25519 public key is 32", len(decoded)))
	}

	if len(problems) > 0 {
		return nil, problems
	}
	return decoded, nil
}

func decodeActivationKeyBody(w http.ResponseWriter, r *http.Request, into any) bool {
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
	if err != nil || len(raw) > MaxEnvelopeBytes {
		writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
		return false
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	// Unknown fields are refused. A misspelled authorization block would otherwise be
	// dropped and the request read as a bootstrap.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		writeJSON(w, http.StatusBadRequest,
			errorBody("the request is not an activation key document: "+err.Error()))
		return false
	}
	return true
}

// activationRegistrar authenticates the caller and checks the activation-key privilege.
func activationRegistrar(w http.ResponseWriter, r *http.Request, creds *identity.Credentials,
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
	if !attested.MayRegisterActivationKeys {
		writeJSON(w, http.StatusForbidden, errorBody(
			"this credential may not register policy activation keys. An activation key "+
				"decides which bundle enforces, and so what every agent in the tenant may "+
				"not do; it is separated from submitting, from issuing authority and from "+
				"registering agent keys. Name the holder in "+
				"GATEWAY_ACTIVATION_KEY_REGISTRARS"))
		return "", "", false
	}
	return tenant, attested.APIIdentity, true
}

// writeActivationKeyError reports a refusal with its stable code.
//
// Forbidden rather than bad request when the document is well-formed and the platform
// declines it: an operator whose authorization was signed by a revoked key has a
// different problem from one who left out an actor, and a single status for both sends
// them looking in the wrong place.
func writeActivationKeyError(w http.ResponseWriter, err error) {
	var refusal *policy.ActivationError
	if errors.As(err, &refusal) {
		status := http.StatusBadRequest
		switch refusal.Code {
		case "ACTIVATION_KEY_UNKNOWN", "ACTIVATION_KEY_REVOKED", "ACTIVATION_KEY_EXPIRED",
			"ACTIVATION_KEY_NOT_YET_VALID", "ACTIVATION_SIGNATURE_INVALID":
			status = http.StatusForbidden
		}
		writeJSON(w, status, map[string]any{"error": refusal.Message, "code": refusal.Code})
		return
	}
	writeJSON(w, http.StatusServiceUnavailable,
		errorBody("the authorizing key could not be read"))
}

// activationKeyEvent builds the record that the set of keys able to authorize policy
// changed.
func activationKeyEvent(name evidence.EventName, key policy.ActivationKey, at time.Time,
	payload map[string]any) evidence.Event {

	payload["key_id"] = key.KeyID
	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("actkey_%s_%s_%d", key.KeyID, name, at.UnixNano()),
		EventName:     name,
		TenantID:      key.TenantID,
		AggregateID:   key.KeyID,
		CorrelationID: key.KeyID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload:       payload,
	}
}

// recordActivationKeyEvent writes one outside a transaction, for revocation.
//
// Best effort, like every other evidence write on an administrative path: the key is
// revoked whether or not the record commits, and refusing a containment action because
// the evidence store is briefly unavailable would fail in the direction that helps
// nobody. Registration is not best effort — it shares a transaction with the grant.
func recordActivationKeyEvent(ctx context.Context, store *evidence.Store,
	name evidence.EventName, key policy.ActivationKey, at time.Time, payload map[string]any) {

	if store == nil {
		return
	}
	_, _ = store.Append(ctx, activationKeyEvent(name, key, at, payload))
}
