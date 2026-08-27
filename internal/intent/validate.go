package intent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ValidationError is one specific reason an envelope was rejected.
//
// Code is stable and is what tests, fixtures and operators key on. Message is for
// humans and may be reworded.
type ValidationError struct {
	Field   string `json:"field"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ValidationErrors is every reason an envelope was rejected, not just the first.
// A caller fixing an integration should not have to submit ten times to find ten
// problems.
type ValidationErrors []ValidationError

func (v ValidationErrors) Error() string {
	parts := make([]string, 0, len(v))
	for _, e := range v {
		parts = append(parts, fmt.Sprintf("%s: %s (%s)", e.Field, e.Code, e.Message))
	}
	return "envelope invalid: " + strings.Join(parts, "; ")
}

// Codes returns the stable codes, in order. Fixtures assert on these.
func (v ValidationErrors) Codes() []string {
	out := make([]string, 0, len(v))
	for _, e := range v {
		out = append(out, e.Code)
	}
	return out
}

// Has reports whether a specific code was raised.
func (v ValidationErrors) Has(code string) bool {
	for _, e := range v {
		if e.Code == code {
			return true
		}
	}
	return false
}

// Decode parses an envelope and validates it.
//
// Decoding is lenient about unknown properties on purpose: producers run ahead of
// consumers under ADR-008, and the schema policy requires forward tolerance.
// Validation is where strictness lives.
//
// A non-nil error means the envelope MUST NOT proceed to authority or policy
// evaluation (spec section 17: invalid envelope -> DENY).
func Decode(data []byte) (*AgentExecutionEnvelope, error) {
	var env AgentExecutionEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, ValidationErrors{{
			Field: "", Code: "ENVELOPE_MALFORMED", Message: err.Error(),
		}}
	}
	if err := env.Validate(); err != nil {
		return nil, err
	}
	return &env, nil
}

// Validate enforces the envelope invariants of spec section 12.3 and ADR-020.
//
// It normalizes what can be normalized deterministically (timestamps to UTC, a
// missing verification level to UNKNOWN) and rejects everything else. It never
// repairs a value by guessing.
func (e *AgentExecutionEnvelope) Validate() error {
	var errs ValidationErrors

	if e.SchemaVersion == "" {
		errs = append(errs, ValidationError{"schema_version", "SCHEMA_VERSION_MISSING",
			"schema_version is required; there is no unversioned envelope"})
	} else if e.SchemaVersion != SchemaVersion {
		errs = append(errs, ValidationError{"schema_version", "SCHEMA_VERSION_UNSUPPORTED",
			fmt.Sprintf("this build implements %s, envelope declares %s", SchemaVersion, e.SchemaVersion)})
	}

	errs = append(errs, requireID("envelope_id", e.EnvelopeID)...)
	errs = append(errs, requireID("idempotency_key", e.IdempotencyKey)...)
	errs = append(errs, requireID("tenant_id", e.TenantID)...)
	errs = append(errs, requireID("authority_grant_id", e.AuthorityGrantID)...)
	errs = append(errs, requireID("principal.principal_id", e.Principal.PrincipalID)...)
	errs = append(errs, requireID("principal.account_id", e.Principal.AccountID)...)
	errs = append(errs, requireID("agent.agent_id", e.Agent.AgentID)...)

	if e.ReceivedAt.IsZero() {
		errs = append(errs, ValidationError{"received_at", "TIMESTAMP_MISSING",
			"received_at is required and must be RFC3339"})
	} else {
		e.ReceivedAt = e.ReceivedAt.UTC()
	}

	errs = append(errs, e.validateAttestation()...)
	errs = append(errs, e.validateRuntimeClaims()...)
	errs = append(errs, e.validateDependencies()...)
	errs = append(errs, e.validateIntent()...)

	if len(errs) > 0 {
		return errs
	}
	return nil
}

func requireID(field, value string) ValidationErrors {
	if strings.TrimSpace(value) == "" {
		return ValidationErrors{{field, "REQUIRED_FIELD_MISSING", field + " is required"}}
	}
	return nil
}

func (e *AgentExecutionEnvelope) validateAttestation() ValidationErrors {
	var errs ValidationErrors
	switch e.Agent.Attestation.Level {
	case "":
		// An absent attestation is A0, not an error. Unknown origin is a real and
		// permitted state; it simply cannot authorize much later on.
		e.Agent.Attestation.Level = AttestationA0
	case AttestationA0, AttestationA1:
	case AttestationA2:
		if strings.TrimSpace(e.Agent.WorkloadIdentity.SpiffeID) == "" {
			errs = append(errs, ValidationError{"agent.workload_identity.spiffe_id",
				"ATTESTATION_A2_WITHOUT_WORKLOAD_IDENTITY",
				"A2 claims a workload-attested identity but no SPIFFE ID is present"})
		}
	case AttestationA3:
		// Spec section 11: A3 must not be simulated or falsely claimed. V0 has no
		// provider attestation mechanism, so the honest answer is to refuse the
		// claim rather than record an unverifiable one.
		errs = append(errs, ValidationError{"agent.attestation.level",
			"ATTESTATION_A3_NOT_SUPPORTED",
			"A3 requires provider-side attestation, which V0 does not implement (ADR-006)"})
	default:
		errs = append(errs, ValidationError{"agent.attestation.level", "ATTESTATION_LEVEL_INVALID",
			"attestation level must be one of A0, A1, A2, A3"})
	}
	return errs
}

// claimFields is fixed and ordered so that validation output is deterministic.
// Ranging a map here would reorder errors between runs.
var claimFields = []string{
	"runtime_claims.model_provider",
	"runtime_claims.model_family",
	"runtime_claims.model_version",
}

func (e *AgentExecutionEnvelope) validateRuntimeClaims() ValidationErrors {
	targets := []*Claim{
		&e.RuntimeClaims.ModelProvider,
		&e.RuntimeClaims.ModelFamily,
		&e.RuntimeClaims.ModelVersion,
	}
	var errs ValidationErrors
	for i, field := range claimFields {
		errs = append(errs, validateClaim(field, targets[i])...)
	}
	return errs
}

func validateClaim(field string, c *Claim) ValidationErrors {
	var errs ValidationErrors

	switch c.Verification {
	case "":
		// ADR-007: a missing verification level is UNKNOWN. It is never DECLARED,
		// and this is the single line that decides it.
		c.Verification = VerificationUnknown
	case VerificationUnknown, VerificationDeclared, VerificationVerified:
	default:
		errs = append(errs, ValidationError{field + ".verification", "VERIFICATION_LEVEL_INVALID",
			"verification must be one of UNKNOWN, DECLARED, VERIFIED"})
		return errs
	}

	if c.Verification == VerificationVerified && strings.TrimSpace(c.EvidenceRef) == "" {
		// INV-008 and INV-014: nothing is verified on its own say-so.
		errs = append(errs, ValidationError{field, "VERIFIED_WITHOUT_EVIDENCE",
			"VERIFIED requires an evidence_ref; a self-declared claim is DECLARED at best"})
	}
	if strings.TrimSpace(c.Value) == "" && c.Verification != VerificationUnknown {
		errs = append(errs, ValidationError{field, "CLAIM_WITHOUT_VALUE",
			"a claim with no value cannot carry a verification level above UNKNOWN"})
	}
	return errs
}

func (e *AgentExecutionEnvelope) validateDependencies() ValidationErrors {
	var errs ValidationErrors
	for i := range e.Dependencies {
		d := &e.Dependencies[i]
		field := fmt.Sprintf("dependencies[%d]", i)

		switch d.Type {
		case DependencyMarketData, DependencyNews, DependencyModel,
			DependencyStrategy, DependencyRuntime, DependencyExecution:
		default:
			errs = append(errs, ValidationError{field + ".type", "DEPENDENCY_TYPE_INVALID",
				"dependency type is not in the V0 catalog"})
		}
		if strings.TrimSpace(d.ID) == "" {
			errs = append(errs, ValidationError{field + ".id", "REQUIRED_FIELD_MISSING",
				"dependency id is required"})
		}
		if d.ObservedAt.IsZero() {
			errs = append(errs, ValidationError{field + ".observed_at", "TIMESTAMP_MISSING",
				"observed_at is required on every dependency assertion (ADR-007)"})
		} else {
			d.ObservedAt = d.ObservedAt.UTC()
		}

		claim := Claim{Value: d.ID, Verification: d.Verification, EvidenceRef: d.EvidenceRef}
		errs = append(errs, validateClaim(field, &claim)...)
		d.Verification = claim.Verification
	}
	return errs
}

func (e *AgentExecutionEnvelope) validateIntent() ValidationErrors {
	var errs ValidationErrors
	in := &e.Intent

	// INV-015: an invalid normalization result cannot proceed to executable policy.
	if _, err := Normalize(in.InstrumentID, in.AssetClass, NormalizedInstrument{}); err != nil {
		var ne *NormalizationError
		if errors.As(err, &ne) {
			errs = append(errs, ValidationError{"intent.instrument_id", ne.Code, ne.Reason})
		} else {
			errs = append(errs, ValidationError{"intent.instrument_id",
				"INSTRUMENT_NORMALIZATION_FAILED", err.Error()})
		}
	}

	switch in.Side {
	case SideBuy, SideSell:
	default:
		errs = append(errs, ValidationError{"intent.side", "SIDE_INVALID", "side must be BUY or SELL"})
	}

	switch in.TimeInForce {
	case "":
		errs = append(errs, ValidationError{"intent.time_in_force", "REQUIRED_FIELD_MISSING",
			"time_in_force is required"})
	case TIFDay, TIFGTC, TIFIOC, TIFFOK:
	default:
		errs = append(errs, ValidationError{"intent.time_in_force", "TIME_IN_FORCE_INVALID",
			"time_in_force must be one of DAY, GTC, IOC, FOK"})
	}

	errs = append(errs, validateSizing(in)...)
	errs = append(errs, validatePrices(in)...)
	return errs
}

// validateSizing enforces spec section 12.3 (exactly one sizing field) and ADR-020
// (which one, per order type).
func validateSizing(in *Intent) ValidationErrors {
	var errs ValidationErrors

	hasNotional := in.Notional != nil
	hasQuantity := in.Quantity != nil

	switch {
	case hasNotional && hasQuantity:
		errs = append(errs, ValidationError{"intent", "SIZING_NOT_EXCLUSIVE",
			"exactly one of quantity or notional may be the primary sizing field"})
	case !hasNotional && !hasQuantity:
		errs = append(errs, ValidationError{"intent", "SIZING_MISSING",
			"one of quantity or notional is required"})
	}

	if hasNotional && *in.Notional <= 0 {
		errs = append(errs, ValidationError{"intent.notional", "SIZING_NOT_POSITIVE",
			"notional must be greater than zero"})
	}
	if hasQuantity && *in.Quantity <= 0 {
		errs = append(errs, ValidationError{"intent.quantity", "SIZING_NOT_POSITIVE",
			"quantity must be greater than zero"})
	}

	switch in.OrderType {
	case OrderMarket:
		// Both sizing fields are meaningful for a market order.
	case OrderLimit, OrderStop, OrderStopLimit:
		// ADR-020: deriving a share count from a quoted price is a trading
		// decision, and the platform does not make trading decisions.
		if hasNotional {
			errs = append(errs, ValidationError{"intent.notional", "NOTIONAL_NOT_ALLOWED_FOR_ORDER_TYPE",
				string(in.OrderType) + " requires quantity; notional must be null (ADR-020)"})
		}
		if !hasQuantity {
			errs = append(errs, ValidationError{"intent.quantity", "QUANTITY_REQUIRED_FOR_ORDER_TYPE",
				string(in.OrderType) + " requires an explicit quantity (ADR-020)"})
		}
	case "":
		errs = append(errs, ValidationError{"intent.order_type", "REQUIRED_FIELD_MISSING",
			"order_type is required"})
	default:
		errs = append(errs, ValidationError{"intent.order_type", "ORDER_TYPE_INVALID",
			"order_type must be one of MARKET, LIMIT, STOP, STOP_LIMIT"})
	}
	return errs
}

func validatePrices(in *Intent) ValidationErrors {
	var errs ValidationErrors

	needsLimit := in.OrderType == OrderLimit || in.OrderType == OrderStopLimit
	needsStop := in.OrderType == OrderStop || in.OrderType == OrderStopLimit

	if needsLimit && in.LimitPrice == nil {
		errs = append(errs, ValidationError{"intent.limit_price", "LIMIT_PRICE_REQUIRED",
			string(in.OrderType) + " requires a limit_price"})
	}
	if !needsLimit && in.LimitPrice != nil {
		errs = append(errs, ValidationError{"intent.limit_price", "LIMIT_PRICE_NOT_ALLOWED",
			string(in.OrderType) + " must not carry a limit_price"})
	}
	if needsStop && in.StopPrice == nil {
		errs = append(errs, ValidationError{"intent.stop_price", "STOP_PRICE_REQUIRED",
			string(in.OrderType) + " requires a stop_price"})
	}
	if !needsStop && in.StopPrice != nil {
		errs = append(errs, ValidationError{"intent.stop_price", "STOP_PRICE_NOT_ALLOWED",
			string(in.OrderType) + " must not carry a stop_price"})
	}
	if in.LimitPrice != nil && *in.LimitPrice <= 0 {
		errs = append(errs, ValidationError{"intent.limit_price", "PRICE_NOT_POSITIVE",
			"limit_price must be greater than zero"})
	}
	if in.StopPrice != nil && *in.StopPrice <= 0 {
		errs = append(errs, ValidationError{"intent.stop_price", "PRICE_NOT_POSITIVE",
			"stop_price must be greater than zero"})
	}
	return errs
}
