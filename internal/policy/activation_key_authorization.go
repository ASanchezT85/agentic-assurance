package policy

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"agentic-assurance/internal/canonicaljson"
)

// Authorizing a key that may authorize policy.
//
// Registering an activation key is the strongest act on this platform. An activation key
// says which bundle enforces, and a bundle says what every agent in the tenant may not
// do — so whoever can add one can hand themselves the power to lift every ceiling the
// customer set, without ever touching a grant or an agent key.
//
// A bearer credential is therefore not enough on its own. Once a tenant holds an
// activation key, a further key may be added only by an authorization signed by a key
// already registered: the customer's existing authority extends itself, rather than the
// platform's operator minting authority for the customer. That is INV-009 read at the key
// layer — the platform never signs what constrains it.
//
// The first key is the exception, and it is the only one. A tenant with no key cannot
// sign anything, so the first registration is a bootstrap performed by a named operator
// credential and recorded as a bootstrap. Every later key is signed for.

// KeyAction is what a key authorization asks for.
type KeyAction string

// ActionRegisterKey adds a key that may authorize activations.
const ActionRegisterKey KeyAction = "REGISTER_ACTIVATION_KEY"

// KeyAuthorizationSchemaVersion is the contract version of the document.
const KeyAuthorizationSchemaVersion = "v0.1"

// KeyAuthorization is a customer's signed statement that another key may authorize
// policy activations.
type KeyAuthorization struct {
	SchemaVersion string `json:"schema_version"`
	TenantID      string `json:"tenant_id"`

	Action KeyAction `json:"action"`

	// SubjectKeyID and SubjectPublicKey are the key being authorized. Both are signed
	// over: an authorization naming only an id would authorize whatever public key
	// later arrived under that name, which is the whole attack it exists to stop.
	SubjectKeyID     string `json:"subject_key_id"`
	SubjectPublicKey string `json:"subject_public_key"`

	// SubjectHolder is who the new key belongs to. A key that authorized a policy
	// change months ago must still be attributable to a person.
	SubjectHolder string `json:"subject_holder"`

	Actor  string `json:"actor"`
	Reason string `json:"reason,omitempty"`

	AuthorizedAt time.Time `json:"authorized_at"`

	// Nonce makes a replay detectable, as it does for an activation: without it a
	// captured authorization could re-register a key the customer has since revoked.
	Nonce string `json:"nonce"`

	Signature Signature `json:"signature"`
}

// Canonical returns the bytes the signature covers.
func (a KeyAuthorization) Canonical() ([]byte, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	object, err := canonicaljson.CanonicalObject(raw)
	if err != nil {
		return nil, err
	}
	delete(object, "signature")
	return canonicaljson.CanonicalizeObject(object)
}

// Sign produces the signature. For customer tooling and for tests; the platform never
// signs one, for the reason in the file comment.
func (a *KeyAuthorization) Sign(priv ed25519.PrivateKey, keyID string) error {
	a.Signature = Signature{Algorithm: AlgorithmEd25519, KeyID: keyID}
	canonical, err := a.Canonical()
	if err != nil {
		return err
	}
	a.Signature.Value = hex.EncodeToString(ed25519.Sign(priv, canonical))
	return nil
}

// PublicKey decodes the subject key.
func (a KeyAuthorization) PublicKey() (ed25519.PublicKey, error) {
	decoded, err := hex.DecodeString(strings.TrimSpace(a.SubjectPublicKey))
	switch {
	case err != nil:
		return nil, activationErr("ACTIVATION_KEY_MALFORMED", "subject_public_key is not hex")
	case len(decoded) == ed25519.PrivateKeySize:
		return nil, activationErr("ACTIVATION_KEY_IS_PRIVATE",
			"subject_public_key is 64 bytes, which is an ed25519 PRIVATE key. It has now "+
				"been sent over the network: generate a new pair and register the 32-byte "+
				"public half")
	case len(decoded) != ed25519.PublicKeySize:
		return nil, activationErr("ACTIVATION_KEY_MALFORMED",
			"subject_public_key is %d bytes; an ed25519 public key is %d",
			len(decoded), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(decoded), nil
}

// Validate checks the document's shape before any cryptography, so a missing actor is
// reported as a missing actor rather than as a signature failure.
func (a KeyAuthorization) Validate() error {
	switch {
	case a.SchemaVersion != KeyAuthorizationSchemaVersion:
		return activationErr("ACTIVATION_SCHEMA_UNSUPPORTED",
			"schema_version %q is not %s", a.SchemaVersion, KeyAuthorizationSchemaVersion)
	case strings.TrimSpace(a.TenantID) == "":
		return activationErr("ACTIVATION_TENANT_MISSING", "tenant_id is required")
	case a.Action != ActionRegisterKey:
		return activationErr("ACTIVATION_ACTION_INVALID",
			"action %q is not %s", a.Action, ActionRegisterKey)
	case strings.TrimSpace(a.SubjectKeyID) == "":
		return activationErr("ACTIVATION_KEY_ID_MISSING", "subject_key_id is required")
	case strings.TrimSpace(a.SubjectHolder) == "":
		return activationErr("ACTIVATION_HOLDER_MISSING",
			"subject_holder is required; a key that authorizes a policy change must be "+
				"attributable to someone months later")
	case strings.TrimSpace(a.Actor) == "":
		return activationErr("ACTIVATION_ACTOR_MISSING",
			"actor is required; adding a key that decides what the platform denies must "+
				"name who authorized it")
	case a.AuthorizedAt.IsZero():
		return activationErr("ACTIVATION_TIME_MISSING", "authorized_at is required")
	case strings.TrimSpace(a.Nonce) == "":
		return activationErr("ACTIVATION_NONCE_MISSING",
			"nonce is required; without one a captured authorization can be replayed")
	case a.Signature.Algorithm != AlgorithmEd25519:
		return activationErr("ACTIVATION_ALGORITHM_UNSUPPORTED",
			"algorithm %q is not %s", a.Signature.Algorithm, AlgorithmEd25519)
	case strings.TrimSpace(a.Signature.KeyID) == "":
		return activationErr("ACTIVATION_KEY_ID_MISSING",
			"the signature must name the key that produced it")
	case strings.TrimSpace(a.Signature.Value) == "":
		return activationErr("ACTIVATION_SIGNATURE_MISSING", "the authorization is not signed")
	case a.Signature.KeyID == strings.TrimSpace(a.SubjectKeyID):
		// A key cannot introduce itself. Allowing it would make the signature
		// self-referential: anyone holding a private key could register it as an
		// authorizer by signing for itself, which is the bootstrap path without the
		// operator credential that gates the bootstrap.
		return activationErr("ACTIVATION_KEY_SELF_SIGNED",
			"key %s cannot authorize its own registration; a new authority is granted "+
				"by an existing one", a.SubjectKeyID)
	}
	if _, err := a.PublicKey(); err != nil {
		return err
	}
	return nil
}

// Verify checks the signature against the signing key's public half.
func (a KeyAuthorization) Verify(pub ed25519.PublicKey) error {
	if err := a.Validate(); err != nil {
		return err
	}
	value, err := hex.DecodeString(a.Signature.Value)
	if err != nil {
		return activationErr("ACTIVATION_SIGNATURE_MALFORMED",
			"the signature is not hex: %v", err)
	}
	canonical, err := a.Canonical()
	if err != nil {
		return activationErr("ACTIVATION_CANONICAL_FAILED", "%v", err)
	}
	if !ed25519.Verify(pub, canonical, value) {
		return activationErr("ACTIVATION_SIGNATURE_INVALID",
			"the authorization was not signed by key %s; a key that may decide which "+
				"policy enforces is granted by the customer's existing key, not by "+
				"whoever holds an API credential", a.Signature.KeyID)
	}
	return nil
}
