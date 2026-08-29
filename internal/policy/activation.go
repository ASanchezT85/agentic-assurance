package policy

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agentic-assurance/internal/canonicaljson"
)

// Authorizing an activation, as opposed to signing a policy.
//
// A bundle's rules were signed and its promotion into production was not. The signed
// object deliberately excludes the Activation block — correctly, because policy content
// must keep a stable identity across promotion — and nothing else authorized the
// promotion. So anyone who could edit the policy file could take a correctly signed
// SHADOW bundle, change one word to ACTIVE, and put it into force without possessing the
// customer's signing key. The signature still verified, because it covered the rules and
// the rules had not changed.
//
// The missing piece is not a stronger signature over the bundle. It is a second signed
// object: the customer's statement that this bundle, identified by content hash, is
// authorized to enforce. Content identity and activation authority are different facts
// and they are signed separately.

// ActivationAction is what the customer authorized.
type ActivationAction string

const (
	// ActionActivate puts a bundle into force.
	ActionActivate ActivationAction = "ACTIVATE"

	// ActionRollback puts a previously-in-force bundle back.
	//
	// A distinct action rather than an inference. A customer restoring the previous
	// bundle during an incident is doing something different from shipping a new one,
	// and an incident review that reads a retreat as a release is reading the incident
	// backwards. It used to be inferred from whether this process had seen the bundle
	// before, which made it a property of process uptime.
	ActionRollback ActivationAction = "ROLLBACK"
)

// AuthorizationSchemaVersion is the contract version of the authorization document.
const AuthorizationSchemaVersion = "v0.1"

// Authorization is the customer's signed statement that a bundle may enforce.
type Authorization struct {
	SchemaVersion string `json:"schema_version"`
	TenantID      string `json:"tenant_id"`

	// BundleID and BundleContentHash identify the bundle. Both, because the id is a
	// name a customer chooses and the hash is what the content actually is: an
	// authorization that named only the id would authorize whatever later took that
	// name.
	BundleID          string `json:"bundle_id"`
	BundleContentHash string `json:"bundle_content_hash"`

	// Prior names the other side of the transition. A rollback that does not say what
	// it is rolling back from cannot be audited, and an activation that names its
	// predecessor lets a reviewer follow the chain without guessing.
	PriorBundleID          string `json:"prior_bundle_id,omitempty"`
	PriorBundleContentHash string `json:"prior_bundle_content_hash,omitempty"`

	Action ActivationAction `json:"action"`
	Actor  string           `json:"actor"`
	Reason string           `json:"reason,omitempty"`

	AuthorizedAt time.Time `json:"authorized_at"`

	// Nonce makes a replay detectable. Without it, an authorization captured once could
	// be presented again later to re-activate a bundle the customer has since retired,
	// and nothing in the document would distinguish the replay from the original.
	Nonce string `json:"nonce"`

	Signature Signature `json:"signature"`
}

// Signature is the ed25519 signature over the canonical authorization.
type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

// AlgorithmEd25519 is the only algorithm V0 accepts.
const AlgorithmEd25519 = "Ed25519"

// ActivationError is a refusal with a stable code, so an operator reading a log knows
// which of several different problems they have.
type ActivationError struct {
	Code    string
	Message string
}

func (e *ActivationError) Error() string { return e.Code + ": " + e.Message }

func activationErr(code, format string, args ...any) *ActivationError {
	return &ActivationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Canonical returns the bytes the signature covers: the whole document except its own
// signature, in the generic canonical form.
func (a Authorization) Canonical() ([]byte, error) {
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
// signs an authorization, because a platform that authorizes its own policy is deciding
// what constrains it (INV-009, ADR-010).
func (a *Authorization) Sign(priv ed25519.PrivateKey, keyID string) error {
	a.Signature = Signature{Algorithm: AlgorithmEd25519, KeyID: keyID}
	canonical, err := a.Canonical()
	if err != nil {
		return err
	}
	a.Signature.Value = hex.EncodeToString(ed25519.Sign(priv, canonical))
	return nil
}

// Validate checks the document's shape before any cryptography.
//
// Separate from Verify so a malformed authorization is reported as malformed rather than
// as a bad signature: an operator chasing a signature failure that is really a missing
// actor looks in the wrong place for a long time.
func (a Authorization) Validate() error {
	switch {
	case a.SchemaVersion != AuthorizationSchemaVersion:
		return activationErr("ACTIVATION_SCHEMA_UNSUPPORTED",
			"schema_version %q is not %s", a.SchemaVersion, AuthorizationSchemaVersion)
	case strings.TrimSpace(a.TenantID) == "":
		return activationErr("ACTIVATION_TENANT_MISSING", "tenant_id is required")
	case strings.TrimSpace(a.BundleID) == "":
		return activationErr("ACTIVATION_BUNDLE_MISSING", "bundle_id is required")
	case strings.TrimSpace(a.BundleContentHash) == "":
		return activationErr("ACTIVATION_HASH_MISSING",
			"bundle_content_hash is required; an authorization that names only an id "+
				"authorizes whatever later takes that name")
	case a.Action != ActionActivate && a.Action != ActionRollback:
		return activationErr("ACTIVATION_ACTION_INVALID",
			"action %q is neither ACTIVATE nor ROLLBACK", a.Action)
	case strings.TrimSpace(a.Actor) == "":
		return activationErr("ACTIVATION_ACTOR_MISSING",
			"actor is required; a change to what the platform denies must name who "+
				"authorized it")
	case a.AuthorizedAt.IsZero():
		return activationErr("ACTIVATION_TIME_MISSING", "authorized_at is required")
	case strings.TrimSpace(a.Nonce) == "":
		return activationErr("ACTIVATION_NONCE_MISSING",
			"nonce is required; without one a captured authorization can be replayed")
	case a.Action == ActionRollback && strings.TrimSpace(a.PriorBundleID) == "":
		return activationErr("ACTIVATION_PRIOR_MISSING",
			"a rollback must name the bundle it is rolling back from")
	case a.Signature.Algorithm != AlgorithmEd25519:
		return activationErr("ACTIVATION_ALGORITHM_UNSUPPORTED",
			"algorithm %q is not %s", a.Signature.Algorithm, AlgorithmEd25519)
	case strings.TrimSpace(a.Signature.KeyID) == "":
		return activationErr("ACTIVATION_KEY_ID_MISSING",
			"the signature must name the key that produced it")
	case strings.TrimSpace(a.Signature.Value) == "":
		return activationErr("ACTIVATION_SIGNATURE_MISSING", "the authorization is not signed")
	}
	return nil
}

// Verify checks the signature against a public key.
func (a Authorization) Verify(pub ed25519.PublicKey) error {
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
			"the authorization was not signed by key %s; a bundle becomes enforceable "+
				"only when the customer has authorized the activation itself, not merely "+
				"signed the rules", a.Signature.KeyID)
	}
	return nil
}

// Authorizes reports whether this authorization covers the given bundle.
//
// Both the id and the content hash, and the tenant. An authorization for tenant A cannot
// activate tenant B's bundle even if the bundle ids happen to match, and an
// authorization for a bundle whose content has since changed does not carry over.
func (a Authorization) Authorizes(b *Bundle) error {
	if b == nil {
		return activationErr("ACTIVATION_NO_BUNDLE", "there is no bundle to authorize")
	}
	if a.TenantID != b.TenantID {
		return activationErr("ACTIVATION_TENANT_MISMATCH",
			"an authorization for %s cannot activate a bundle belonging to %s",
			a.TenantID, b.TenantID)
	}
	if a.BundleID != b.BundleID {
		return activationErr("ACTIVATION_BUNDLE_MISMATCH",
			"the authorization names bundle %s and the bundle is %s", a.BundleID, b.BundleID)
	}
	if a.BundleContentHash != b.ContentHash {
		return activationErr("ACTIVATION_CONTENT_MISMATCH",
			"the authorization names content %s and the bundle's content is %s; the "+
				"rules changed after the customer authorized them",
			short(a.BundleContentHash), short(b.ContentHash))
	}
	return nil
}

func short(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
