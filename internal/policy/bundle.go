package policy

import (
	"agentic-assurance/internal/money"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"agentic-assurance/internal/intent"
)

// Status is the policy lifecycle of spec sections 15.2 and 43.
type Status string

const (
	StatusDraft      Status = "DRAFT"
	StatusValidated  Status = "VALIDATE"
	StatusCompiled   Status = "COMPILE"
	StatusSigned     Status = "SIGN"
	StatusSimulated  Status = "SIMULATE"
	StatusShadow     Status = "SHADOW"
	StatusCanary     Status = "CANARY"
	StatusActive     Status = "ACTIVE"
	StatusRolledBack Status = "ROLLED_BACK"
)

// allowedTransitions is the lifecycle, written down once.
//
// It is a forward pipeline with one escape hatch: anything past SIGN can be rolled
// back. There is no path from DRAFT to ACTIVE, which is INV-010 expressed as data
// rather than as a code review convention.
var allowedTransitions = map[Status][]Status{
	StatusDraft:     {StatusValidated},
	StatusValidated: {StatusCompiled},
	StatusCompiled:  {StatusSigned},
	StatusSigned:    {StatusSimulated, StatusRolledBack},
	StatusSimulated: {StatusShadow, StatusRolledBack},
	StatusShadow:    {StatusCanary, StatusRolledBack},
	StatusCanary:    {StatusActive, StatusRolledBack},
	StatusActive:    {StatusRolledBack},
	// Terminal. A rolled-back bundle is never resurrected; a new version is
	// created instead (spec section 43: production policy is never edited in place).
	StatusRolledBack: nil,
}

// CompiledRule is a rule in executable form. There is no YAML here, and no string
// that has to be parsed again at decision time.
type CompiledRule struct {
	ID     string `json:"id"`
	Action Action `json:"action"`

	WhenAssetClass    intent.AssetClass `json:"when_asset_class,omitempty"`
	WhenSide          intent.Side       `json:"when_side,omitempty"`
	WhenOrderType     intent.OrderType  `json:"when_order_type,omitempty"`
	WhenInstruments   []string          `json:"when_instruments,omitempty"`
	WhenNotionalGT    *money.Amount     `json:"when_notional_gt,omitempty"`
	WhenNotionalGTE   *money.Amount     `json:"when_notional_gte,omitempty"`
	WhenNotionalLT    *money.Amount     `json:"when_notional_lt,omitempty"`
	WhenNotionalLTE   *money.Amount     `json:"when_notional_lte,omitempty"`
	WhenExtendedHours *bool             `json:"when_extended_hours,omitempty"`

	RequireNotionalLTE *money.Amount `json:"require_notional_lte,omitempty"`
	RequireNotionalGTE *money.Amount `json:"require_notional_gte,omitempty"`

	// NeedsNotional is computed at compile time so the evaluator never has to work
	// out whether a rule depends on order size.
	NeedsNotional bool `json:"needs_notional"`
}

// Activation is the staged-deployment metadata of spec section 43.
type Activation struct {
	Status           Status     `json:"status"`
	ActivatedAt      *time.Time `json:"activated_at,omitempty"`
	ActivatedBy      string     `json:"activated_by,omitempty"`
	RolledBackAt     *time.Time `json:"rolled_back_at,omitempty"`
	RolledBackBy     string     `json:"rolled_back_by,omitempty"`
	RollbackReason   string     `json:"rollback_reason,omitempty"`
	PreviousBundleID string     `json:"previous_bundle_id,omitempty"`
}

// Bundle is a compiled, hashable, signable policy.
//
// ContentHash covers the fields that change what is enforced, and nothing else.
// Lifecycle transitions and activation metadata are deliberately outside it: moving
// a bundle from SHADOW to ACTIVE must not change its identity, or every promotion
// would look like a different policy.
type Bundle struct {
	BundleID string         `json:"bundle_id"`
	TenantID string         `json:"tenant_id"`
	Policy   string         `json:"policy"`
	Version  int            `json:"version"`
	Rules    []CompiledRule `json:"rules"`

	ContentHash string `json:"content_hash"`
	Signature   string `json:"signature"`
	SignedBy    string `json:"signed_by,omitempty"`

	CompiledAt time.Time  `json:"compiled_at"`
	Activation Activation `json:"activation"`
}

// hashableBundle is the exact subject of ContentHash and of the signature.
type hashableBundle struct {
	BundleID string         `json:"bundle_id"`
	TenantID string         `json:"tenant_id"`
	Policy   string         `json:"policy"`
	Version  int            `json:"version"`
	Rules    []CompiledRule `json:"rules"`
}

// canonicalBytes produces the byte sequence that is hashed and signed.
//
// Determinism matters more than readability here: the same rules must produce the
// same hash on every machine and every run, or the signature is meaningless. Struct
// field order in encoding/json is declaration order, and there are no maps in the
// hashed subject, so this is stable by construction.
func (b *Bundle) canonicalBytes() ([]byte, error) {
	return json.Marshal(hashableBundle{
		BundleID: b.BundleID,
		TenantID: b.TenantID,
		Policy:   b.Policy,
		Version:  b.Version,
		Rules:    b.Rules,
	})
}

// ComputeHash returns the content hash without storing it.
func (b *Bundle) ComputeHash() (string, error) {
	raw, err := b.canonicalBytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Sign hashes the bundle and signs the hash, moving it from COMPILE to SIGN.
func (b *Bundle) Sign(key ed25519.PrivateKey, signedBy string, at time.Time) error {
	if b.Activation.Status != StatusCompiled {
		return fmt.Errorf("cannot sign a bundle in status %s", b.Activation.Status)
	}
	hash, err := b.ComputeHash()
	if err != nil {
		return err
	}
	b.ContentHash = hash
	b.Signature = hex.EncodeToString(ed25519.Sign(key, []byte(hash)))
	b.SignedBy = signedBy
	b.Activation.Status = StatusSigned
	return nil
}

// VerifySignature recomputes the hash and checks the signature over it.
//
// It recomputes rather than trusting the stored hash. Verifying a signature over a
// hash the bundle carries would confirm only that the two agree with each other,
// which a tampered bundle can arrange trivially.
func (b *Bundle) VerifySignature(pub ed25519.PublicKey) error {
	recomputed, err := b.ComputeHash()
	if err != nil {
		return err
	}
	if recomputed != b.ContentHash {
		return fmt.Errorf("content hash mismatch: bundle claims %s, contents hash to %s",
			b.ContentHash, recomputed)
	}
	sig, err := hex.DecodeString(b.Signature)
	if err != nil {
		return fmt.Errorf("signature is not valid hex: %w", err)
	}
	if !ed25519.Verify(pub, []byte(recomputed), sig) {
		return fmt.Errorf("signature does not verify against the content hash")
	}
	return nil
}

// Transition advances the lifecycle, refusing anything the pipeline does not allow.
func (b *Bundle) Transition(to Status, at time.Time, actor string) error {
	from := b.Activation.Status
	for _, allowed := range allowedTransitions[from] {
		if allowed != to {
			continue
		}
		// INV-010: reaching production requires a version, a hash and a signature.
		// The lifecycle order alone does not prove they exist.
		if to == StatusActive {
			if err := b.readyForProduction(); err != nil {
				return err
			}
			t := at.UTC()
			b.Activation.ActivatedAt = &t
			b.Activation.ActivatedBy = actor
		}
		if to == StatusRolledBack {
			t := at.UTC()
			b.Activation.RolledBackAt = &t
			b.Activation.RolledBackBy = actor
		}
		b.Activation.Status = to
		return nil
	}
	return fmt.Errorf("illegal policy lifecycle transition %s -> %s", from, to)
}

func (b *Bundle) readyForProduction() error {
	switch {
	case b.Version < 1:
		return fmt.Errorf("bundle has no version; a policy cannot reach production unversioned (INV-010)")
	case b.ContentHash == "":
		return fmt.Errorf("bundle has no content hash (INV-010)")
	case b.Signature == "":
		return fmt.Errorf("bundle is not signed (INV-010)")
	case len(b.Rules) == 0:
		return fmt.Errorf("bundle has no rules; an empty policy enforces nothing")
	}
	return nil
}

// Rollback is the audited escape hatch of spec section 43.
func (b *Bundle) Rollback(at time.Time, actor, reason string) error {
	if reason == "" {
		return fmt.Errorf("a rollback without a reason cannot be explained later")
	}
	if err := b.Transition(StatusRolledBack, at, actor); err != nil {
		return err
	}
	b.Activation.RollbackReason = reason
	return nil
}

// Enforcing reports whether this bundle's decisions bind production.
//
// SHADOW and CANARY are not production enforcement: shadow records what would have
// happened (spec section 42), and a bundle in either state must never be mistaken
// for the active one.
func (b *Bundle) Enforcing() bool { return b.Activation.Status == StatusActive }
