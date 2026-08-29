// Package intent holds the canonical financial-intent contract.
//
// AgentExecutionEnvelope is the single object every inbound adapter must produce
// before any policy runs (ADR-002). Nothing downstream reads a protocol-specific
// field.
//
// The JSON Schema in packages/envelope-schema is the published contract for
// producers. This package is the enforcement. schema_sync_test.go fails if the two
// stop agreeing.
package intent

import (
	"time"
)

// SchemaVersion is the envelope contract version this package implements.
const SchemaVersion = "0.1"

// VerificationLevel is the V0 provenance taxonomy (spec §11). There are exactly
// three levels and the system never silently promotes between them (P-004, INV-008).
type VerificationLevel string

const (
	// VerificationUnknown is the zero value on purpose: an assertion that says
	// nothing about its provenance is UNKNOWN, never DECLARED.
	VerificationUnknown  VerificationLevel = "UNKNOWN"
	VerificationDeclared VerificationLevel = "DECLARED"
	VerificationVerified VerificationLevel = "VERIFIED"
)

// AttestationLevel describes the workload, never the model (ADR-006).
type AttestationLevel string

const (
	AttestationA0 AttestationLevel = "A0" // unknown origin
	AttestationA1 AttestationLevel = "A1" // authenticated app/API identity
	AttestationA2 AttestationLevel = "A2" // workload-attested identity
	AttestationA3 AttestationLevel = "A3" // provider-attested runtime/model identity
)

// Claim is any assertion that carries provenance (ADR-007). A claim without a
// verification level is UNKNOWN.
type Claim struct {
	Value        string            `json:"value"`
	Verification VerificationLevel `json:"verification"`
	EvidenceRef  string            `json:"evidence_ref,omitempty"`
}

// Verified reports whether this claim may be treated as verified. It is a method
// rather than a field comparison so there is exactly one place that answers the
// question, and it can never be answered "yes" without an evidence reference.
func (c Claim) Verified() bool {
	return c.Verification == VerificationVerified && c.EvidenceRef != ""
}

type WorkloadIdentity struct {
	SpiffeID string `json:"spiffe_id,omitempty"`
}

type Attestation struct {
	Level       AttestationLevel `json:"level"`
	Method      string           `json:"method,omitempty"`
	EvidenceRef string           `json:"evidence_ref,omitempty"`
}

type Agent struct {
	AgentID          string           `json:"agent_id"`
	WorkloadIdentity WorkloadIdentity `json:"workload_identity"`
	Attestation      Attestation      `json:"attestation"`
}

type Principal struct {
	PrincipalID string `json:"principal_id"`
	AccountID   string `json:"account_id"`
}

// RuntimeClaims are what the agent says about itself. They are DECLARED at best
// unless a provider attests them (ADR-006, INV-014).
type RuntimeClaims struct {
	ModelProvider Claim `json:"model_provider"`
	ModelFamily   Claim `json:"model_family"`
	ModelVersion  Claim `json:"model_version"`
}

// DependencyType enumerates what an intent can depend on. Concentration is computed
// per type (§25).
type DependencyType string

const (
	DependencyMarketData DependencyType = "MARKET_DATA"
	DependencyNews       DependencyType = "NEWS"
	DependencyModel      DependencyType = "MODEL"
	DependencyStrategy   DependencyType = "STRATEGY"
	DependencyRuntime    DependencyType = "RUNTIME"
	DependencyExecution  DependencyType = "EXECUTION_ADAPTER"
)

type Dependency struct {
	Type         DependencyType    `json:"type"`
	ID           string            `json:"id"`
	Verification VerificationLevel `json:"verification"`
	ObservedAt   time.Time         `json:"observed_at"`
	EvidenceRef  string            `json:"evidence_ref,omitempty"`
}

type AssetClass string

const (
	AssetEquity AssetClass = "EQUITY"
	AssetETF    AssetClass = "ETF"
	AssetOption AssetClass = "OPTION"
	AssetCrypto AssetClass = "CRYPTO"
)

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type OrderType string

const (
	OrderMarket    OrderType = "MARKET"
	OrderLimit     OrderType = "LIMIT"
	OrderStop      OrderType = "STOP"
	OrderStopLimit OrderType = "STOP_LIMIT"
)

type TimeInForce string

const (
	TIFDay TimeInForce = "DAY"
	TIFGTC TimeInForce = "GTC"
	TIFIOC TimeInForce = "IOC"
	TIFFOK TimeInForce = "FOK"
)

// Intent is the economic action itself.
//
// Notional and Quantity are pointers so that "absent" and "zero" are different
// facts. Exactly one is the primary sizing field (§12.3), and which one is
// permitted depends on OrderType (ADR-020).
type Intent struct {
	AssetClass    AssetClass  `json:"asset_class"`
	InstrumentID  string      `json:"instrument_id"`
	Side          Side        `json:"side"`
	OrderType     OrderType   `json:"order_type"`
	Notional      *float64    `json:"notional"`
	Quantity      *float64    `json:"quantity"`
	LimitPrice    *float64    `json:"limit_price"`
	StopPrice     *float64    `json:"stop_price"`
	TimeInForce   TimeInForce `json:"time_in_force"`
	ExtendedHours bool        `json:"extended_hours"`
}

type Lineage struct {
	ParentIntentID string `json:"parent_intent_id,omitempty"`
	StrategyID     string `json:"strategy_id,omitempty"`
}

type Context struct {
	PortfolioSnapshotID string `json:"portfolio_snapshot_id,omitempty"`
	MarketSnapshotID    string `json:"market_snapshot_id,omitempty"`
}

// Signature binds an executable envelope to an agent's signing key.
//
// KeyID is what makes the binding checkable: a key is registered to one tenant and one
// agent, so a signature that names a key proves which agent signed rather than which
// customer's transport carried it. Without it the platform knew the tenant from the
// credential and took the envelope's word for the agent — while the authority grant is
// scoped to exactly that agent.
type Signature struct {
	Algorithm string `json:"algorithm,omitempty"`
	KeyID     string `json:"key_id,omitempty"`
	Value     string `json:"value,omitempty"`
}

// AgentExecutionEnvelope is the canonical contract (ADR-002).
//
// Unknown JSON properties are accepted and ignored: a producer running ahead of a
// consumer is normal under ADR-008, so decoding is deliberately lenient while
// validation is strict.
type AgentExecutionEnvelope struct {
	SchemaVersion  string        `json:"schema_version"`
	EnvelopeID     string        `json:"envelope_id"`
	IdempotencyKey string        `json:"idempotency_key"`
	CorrelationID  string        `json:"correlation_id"`
	ReceivedAt     time.Time     `json:"received_at"`
	TenantID       string        `json:"tenant_id"`
	Principal      Principal     `json:"principal"`
	Agent          Agent         `json:"agent"`
	RuntimeClaims  RuntimeClaims `json:"runtime_claims"`

	// AuthorityGrantID is mandatory for executable intents (§12.3).
	AuthorityGrantID string `json:"authority_grant_id"`

	Dependencies []Dependency `json:"dependencies"`
	Intent       Intent       `json:"intent"`
	Lineage      Lineage      `json:"lineage"`
	Context      Context      `json:"context"`
	Signature    Signature    `json:"signature"`
}
