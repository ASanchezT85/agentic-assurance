package identity

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"agentic-assurance/internal/intent"
)

// Envelope signing: who signed this intent, and may that key speak for this agent.
//
// Transport identity establishes the tenant. The agent was a claim in the body — the
// platform knew which customer was calling and took the envelope's word for which
// agent it was, while the authority grant is scoped to exactly that agent. A key
// registered to one agent must never verify an envelope claiming another, and that
// binding is what this file exists for.
//
// It proves control of an agent signing key. It does not prove which model produced
// the reasoning: inferring that from a key would be the inference ADR-006 and INV-014
// forbid.

// SignatureError names why an envelope's signature was refused. The codes are stable
// and reach the caller, because "invalid signature" and "the key was revoked" are
// different operational stories.
type SignatureError struct {
	Code   string
	Reason string
}

func (e *SignatureError) Error() string { return e.Code + ": " + e.Reason }

// ErrNoSigningKeys is returned when no registry is configured at all.
var ErrNoSigningKeys = errors.New("no agent signing key registry is configured")

// AgentKey is one registered verification key.
type AgentKey struct {
	TenantID  string
	AgentID   string
	KeyID     string
	Algorithm string
	PublicKey []byte

	Status     string
	ValidFrom  time.Time
	ValidUntil time.Time
	RevokedAt  *time.Time
}

// AlgorithmEd25519 is the only algorithm V0 accepts.
//
// One algorithm rather than a negotiated set: an algorithm field a caller controls is
// a downgrade attack unless the platform decides which values are acceptable, and one
// value is the smallest way to decide.
const AlgorithmEd25519 = "Ed25519"

// InForce reports whether a key may verify at a moment.
func (k AgentKey) InForce(at time.Time) (bool, string) {
	switch {
	case k.RevokedAt != nil && !at.Before(*k.RevokedAt):
		return false, "SIGNATURE_KEY_REVOKED"
	case k.Status != "" && k.Status != "ACTIVE":
		return false, "SIGNATURE_KEY_REVOKED"
	case !k.ValidFrom.IsZero() && at.Before(k.ValidFrom):
		return false, "SIGNATURE_KEY_UNKNOWN"
	case !k.ValidUntil.IsZero() && !at.Before(k.ValidUntil):
		return false, "SIGNATURE_KEY_EXPIRED"
	}
	return true, ""
}

// KeySource looks up a verification key.
//
// Scoped by tenant, agent and key id together. Looking up by key id alone would let a
// key registered for one agent verify another's envelope, which is the binding this
// whole mechanism exists to establish.
type KeySource interface {
	AgentKey(ctx Context, tenantID, agentID, keyID string) (*AgentKey, error)
}

// Context is the subset of context.Context this package uses. Declared here so the
// enforcement path keeps its narrow imports.
type Context = interface {
	Deadline() (deadline time.Time, ok bool)
	Done() <-chan struct{}
	Err() error
	Value(key any) any
}

// VerifyEnvelopeSignature checks that the raw envelope was signed by a key registered
// to the tenant and agent it claims.
//
// The raw bytes rather than the decoded struct: the signature covers what the caller
// actually sent, canonicalized, and a struct that has already been through a decoder
// is a re-rendering of it.
func VerifyEnvelopeSignature(ctx Context, keys KeySource, raw []byte,
	env *intent.AgentExecutionEnvelope, at time.Time) error {

	if env == nil {
		return &SignatureError{"SIGNATURE_MISSING", "no envelope to verify"}
	}
	if keys == nil {
		// Fail closed. A platform that cannot check signatures must not decide that
		// every signature is fine.
		return &SignatureError{"SIGNATURE_KEY_UNKNOWN",
			"no agent signing key registry is configured, so no signature can be verified"}
	}

	signature := env.Signature
	if signature.Value == "" || signature.KeyID == "" {
		return &SignatureError{"SIGNATURE_MISSING",
			"an executable intent must carry a signature naming the key that produced it"}
	}
	if signature.Algorithm != AlgorithmEd25519 {
		return &SignatureError{"SIGNATURE_ALGORITHM_UNSUPPORTED",
			fmt.Sprintf("%q is not an accepted algorithm; this build verifies %s",
				signature.Algorithm, AlgorithmEd25519)}
	}

	key, err := keys.AgentKey(ctx, env.TenantID, env.Agent.AgentID, signature.KeyID)
	if err != nil {
		return &SignatureError{"SIGNATURE_KEY_UNKNOWN",
			"the signing key could not be read: " + err.Error()}
	}
	if key == nil {
		// Deliberately the same answer whether the key does not exist or belongs to
		// another agent. Telling a caller which would let them enumerate a tenant's
		// agents and keys.
		return &SignatureError{"SIGNATURE_KEY_UNKNOWN",
			fmt.Sprintf("no key %q is registered for this agent", signature.KeyID)}
	}
	if inForce, code := key.InForce(at); !inForce {
		return &SignatureError{code,
			fmt.Sprintf("key %q may not sign at this time", signature.KeyID)}
	}
	if len(key.PublicKey) != ed25519.PublicKeySize {
		return &SignatureError{"SIGNATURE_KEY_UNKNOWN",
			"the registered key is not a usable Ed25519 public key"}
	}

	value, err := hex.DecodeString(signature.Value)
	if err != nil {
		return &SignatureError{"SIGNATURE_INVALID", "the signature is not hex"}
	}

	canonical, err := intent.Canonical(raw)
	if err != nil {
		return &SignatureError{"SIGNATURE_INVALID", err.Error()}
	}

	if !ed25519.Verify(ed25519.PublicKey(key.PublicKey), canonical, value) {
		// One message for every way this fails. A caller learns their signature did
		// not verify, not which byte the platform disagreed about.
		return &SignatureError{"SIGNATURE_INVALID",
			"the signature does not verify against the canonical envelope"}
	}
	return nil
}

// SignEnvelope produces the signature for raw envelope bytes. It exists for tests and
// for the agent SDK; nothing on the enforcement path calls it.
func SignEnvelope(raw []byte, priv ed25519.PrivateKey) (string, error) {
	canonical, err := intent.Canonical(raw)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(ed25519.Sign(priv, canonical)), nil
}

// MemoryKeys is an in-process key registry.
//
// It exists so tests exercise the same verification call the gateway makes rather than
// a reimplementation of it. The registry that has to hold across replicas is the
// PostgreSQL one.
type MemoryKeys struct {
	keys map[string]AgentKey
}

func NewMemoryKeys() *MemoryKeys { return &MemoryKeys{keys: map[string]AgentKey{}} }

func (m *MemoryKeys) Add(k AgentKey) {
	m.keys[k.TenantID+"\x00"+k.AgentID+"\x00"+k.KeyID] = k
}

func (m *MemoryKeys) AgentKey(_ Context, tenantID, agentID, keyID string) (*AgentKey, error) {
	if m == nil {
		return nil, nil
	}
	key, ok := m.keys[tenantID+"\x00"+agentID+"\x00"+keyID]
	if !ok {
		return nil, nil
	}
	return &key, nil
}
