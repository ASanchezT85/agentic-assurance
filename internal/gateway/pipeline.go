// Package gateway is the composition root of the enforcement plane.
//
// Every other internal package does one thing and knows nothing about the others.
// This one chains them, in the order docs/architecture/hot-path.md describes, and
// that document is the specification for this file rather than a description of it.
// If the two disagree, the document is right and this is a bug.
//
// Nothing new is decided here. The pipeline validates, identifies, checks
// idempotency, evaluates authority, reconstructs parent intent, evaluates policy,
// and submits. Each of those already knows how to say no, and the pipeline's whole
// job is to ask them in the right order and stop at the first refusal.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// Stage names where a submission stopped. They are stable and they appear in
// evidence, so an operator reading a denial knows which check refused it without
// reading code.
const (
	StageValidation  = "VALIDATION"
	StageIdentity    = "IDENTITY"
	StageIdempotency = "IDEMPOTENCY"
	StageAuthority   = "AUTHORITY"
	StagePolicy      = "POLICY"
	StageExecution   = "EXECUTION"
)

// Result is what a submission produced.
type Result struct {
	Accepted bool

	// Stage names where the pipeline stopped. Empty when it ran to the end.
	Stage string

	// Code is the stable reason. It comes from whichever component refused, never
	// invented here, so an operator can trace it back to the rule that produced it.
	Code    string
	Reason  string
	Details []string

	EnvelopeID    string
	CorrelationID string

	// envelope is the decoded envelope, kept so a refusal can still be observed by
	// the analytical plane. Not serialised: the API returns a decision, not an echo
	// of the request.
	envelope *intent.AgentExecutionEnvelope

	// Attested is what identity actually established, never what the envelope
	// claimed (INV-001).
	Attested identity.Attested

	Authority *authority.Decision
	Policy    *policy.Decision
	Outcome   *execution.Outcome

	// Replayed marks a duplicate served from the idempotency record rather than
	// re-executed (spec section 17).
	Replayed bool

	DecidedAt time.Time
}

// PolicyProvider supplies the active bundle for a tenant.
//
// An interface because there is no policy bundle store: Phase 4 built the lifecycle
// and nothing persists it. A file-backed provider is enough to run the pipeline and
// is honest about what it is; the store belongs with the API that manages bundles.
type PolicyProvider interface {
	Active(ctx context.Context, tenantID string) (*policy.Bundle, error)
}

// GrantProvider supplies an authority grant.
type GrantProvider interface {
	Load(ctx context.Context, tenantID, grantID string) (*authority.Grant, error)
}

// SymbolResolver maps canonical instrument identity to a venue symbol.
//
// The pipeline holds it rather than the adapter because instrument reference data
// belongs to the platform (spec section 13), and because two adapters would
// otherwise each need their own copy of the same mapping.
type SymbolResolver interface {
	SymbolFor(instrumentID string) (string, bool)
}

// EvidenceSink records what happened. It is deliberately fire-and-forget from the
// pipeline's point of view: spec section 17 requires production to continue when
// telemetry is unavailable, so a failure to record must never fail a decision.
type EvidenceSink interface {
	Append(ctx context.Context, e evidence.Event) (bool, error)
}

// Pipeline is the enforcement plane's composition root.
type Pipeline struct {
	Identity *identity.Verifier
	Grants   GrantProvider
	Policies PolicyProvider

	// Usage is consumed notional per grant. A grant with a rolling or daily limit
	// denies without one, which is correct and unusable: the limit cannot be
	// enforced, so it must not be waved through.
	Usage authority.UsageSource

	// UsageRecorder is written to after a submission. Separate from Usage because
	// the evaluator must only ever read: a component that could record its own
	// usage could also record none.
	UsageRecorder authority.Recorder
	Execution     *execution.Service
	Symbols       SymbolResolver

	// Evidence is optional. Losing it costs the audit trail, not the decision.
	Evidence EvidenceSink

	// Telemetry feeds the analytical plane. Optional, off the hot path, and never
	// able to fail a decision: ClickHouse is forbidden from the enforcement path
	// (INV-005).
	Telemetry *Telemetry

	// Parent tracks recent intents so a fragmented economic intent is reconstructed
	// (spec section 20). Optional: without it the pipeline still enforces, and the
	// parent intent simply is not computed.
	Parent *ParentTracker

	Now func() time.Time
}

func (p *Pipeline) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Submit runs one intent through the enforcement plane.
//
// The order is docs/architecture/hot-path.md, and the reason each step is where it is
// belongs to that document. The one worth repeating here: idempotency is checked
// before authority and policy, so a duplicate returns the prior outcome rather than
// being re-evaluated against a grant that may since have expired.
func (p *Pipeline) Submit(ctx context.Context, raw []byte, presented identity.Presented) Result {
	at := p.now().UTC()

	// 3. Envelope validation.
	env, err := intent.Decode(raw)
	if err != nil {
		return p.refuse(StageValidation, "ENVELOPE_INVALID", err, at, nil)
	}

	result := Result{
		EnvelopeID:    env.EnvelopeID,
		CorrelationID: env.CorrelationID,
		DecidedAt:     at,
		envelope:      env,
	}

	// 4. Identity and attestation, from evidence rather than from the envelope.
	established := p.Identity.Resolve(presented)
	result.Attested = established

	if err := identity.CheckClaim(env.Agent.Attestation.Level, established); err != nil {
		p.record(ctx, env, evidence.IdentityFailed, at, map[string]any{
			"claimed":     string(env.Agent.Attestation.Level),
			"established": string(established.Level),
			"reason":      err.Error(),
		})
		return p.deny(result, StageIdentity, "ATTESTATION_CLAIM_EXCEEDS_EVIDENCE", err.Error())
	}
	if err := identity.RequireExecutable(established); err != nil {
		p.record(ctx, env, evidence.IdentityFailed, at, map[string]any{
			"established": string(established.Level),
			"reason":      err.Error(),
		})
		return p.deny(result, StageIdentity, "UNAUTHENTICATED_WORKLOAD", err.Error())
	}

	// Then the tenant the caller was authenticated for, against the one the envelope
	// claims. After executability, because whether a caller can act at all is the
	// more fundamental question: asking which tenant an unauthenticated caller
	// belongs to answers the wrong one, and it left RequireExecutable unreachable.
	//
	// Without this check an authenticated caller could name any tenant and every
	// lookup that follows would use it: the grant, the policy bundle, the idempotency
	// record and the row level security setting all take env.TenantID. The database
	// half of INV-007 was enforced the whole time and isolated correctly to a tenant
	// nobody had established.
	if err := identity.RequireTenant(established, env.TenantID); err != nil {
		p.record(ctx, env, evidence.IdentityFailed, at, map[string]any{
			"claimed_tenant": env.TenantID,
			"reason":         err.Error(),
		})
		return p.deny(result, StageIdentity, "TENANT_NOT_AUTHENTICATED", err.Error())
	}

	p.record(ctx, env, evidence.IntentReceived, at, map[string]any{
		"instrument_id": env.Intent.InstrumentID,
		"side":          string(env.Intent.Side),
		"order_type":    string(env.Intent.OrderType),
	})
	p.record(ctx, env, evidence.IdentityVerified, at, map[string]any{
		"level":     string(established.Level),
		"method":    established.Method,
		"spiffe_id": established.SpiffeID.String(),
		// Which credential acted, not only that one did. For an A1 caller the SPIFFE
		// id is empty, so without this the chain says an authenticated caller
		// submitted the order and cannot say which: "who placed this" had no answer
		// for the level most callers actually reach.
		"api_identity": established.APIIdentity,
		"tenant_id":    established.TenantID,
	})

	// 5. Idempotency, before anything is evaluated. A duplicate must return the
	// answer the first caller was given, not a fresh evaluation that could differ.
	if prior, found := p.priorOutcome(ctx, env); found {
		// Recorded, because otherwise the chain shows an intent arriving and no
		// decision following it: the outcome was returned, not produced.
		p.record(ctx, env, evidence.IntentReplayed, at, map[string]any{
			"idempotency_key": env.IdempotencyKey,
			"client_order_id": prior.ClientOrderID,
			"broker_order_id": prior.BrokerOrderID,
			"state":           string(prior.State),
		})

		result.Accepted = prior.State != broker.StateRejected
		result.Replayed = true
		result.Outcome = &prior
		result.Stage = StageIdempotency
		result.Code = "DUPLICATE"
		result.Reason = "this idempotency key already has an outcome; returning it unchanged"
		return result
	}

	// 6. Authority.
	grant, err := p.Grants.Load(ctx, env.TenantID, env.AuthorityGrantID)
	if err != nil {
		// An unreadable grant store denies. Spec section 17: hard policy unavailable
		// means DENY, and authority is the harder half of it.
		return p.deny(result, StageAuthority, "GRANT_UNAVAILABLE",
			"the authority grant could not be read: "+err.Error())
	}

	authDecision := authority.Evaluate(ctx, env, grant, p.Usage, at)
	result.Authority = &authDecision
	p.record(ctx, env, evidence.AuthorityEvaluated, at, map[string]any{
		"allowed":  authDecision.Allowed,
		"code":     authDecision.Code,
		"grant_id": authDecision.GrantID,
	})
	if !authDecision.Allowed {
		return p.deny(result, StageAuthority, authDecision.Code, authDecision.Reason)
	}

	// 7. Parent intent. It informs; it does not decide. Spec section 20 is explicit
	// that this is a reconstruction with confidence, not a finding of fact, so a
	// pipeline that denied on it would be acting on a guess.
	if p.Parent != nil {
		if parent, ok := p.Parent.Observe(env); ok {
			p.record(ctx, env, evidence.IntentParentLinked, at, map[string]any{
				"parent_intent_id":  parent.ParentIntentID,
				"child_count":       parent.ChildCount,
				"agent_count":       parent.AgentCount,
				"gross_notional":    parent.GrossNotional,
				"confidence":        parent.Confidence,
				"exposure_complete": parent.ExposureComplete(),
			})
		}
	}

	// 8. Hard policy.
	bundle, err := p.Policies.Active(ctx, env.TenantID)
	if err != nil {
		return p.deny(result, StagePolicy, "POLICY_UNAVAILABLE",
			"the active policy bundle could not be read: "+err.Error())
	}

	policyDecision := policy.Evaluate(bundle, env, at)
	result.Policy = &policyDecision
	p.record(ctx, env, evidence.PolicyEvaluated, at, map[string]any{
		"action":       string(policyDecision.Action),
		"decided_by":   policyDecision.DecidedBy,
		"bundle_id":    policyDecision.BundleID,
		"bundle_hash":  policyDecision.ContentHash,
		"bundle_state": string(policyDecision.Status),
	})

	if policyDecision.Action != policy.ActionAllow && policyDecision.Action != policy.ActionObserve {
		// DENY, REQUIRE_APPROVAL, THROTTLE and DELAY all stop the submission here.
		// Only the last three are recoverable, and recovering them needs an approval
		// workflow that does not exist; refusing is the honest behaviour until it
		// does, and the code says which action caused it.
		return p.deny(result, StagePolicy, string(policyDecision.Action), policyDecision.Reason)
	}

	// 9-11. Idempotency claim, submission, reconciliation. All three live in
	// execution.Service, which is the only thing allowed to talk to a venue.
	req, err := p.orderRequest(env)
	if err != nil {
		return p.deny(result, StageExecution, "UNSUPPORTED_ORDER", err.Error())
	}

	p.record(ctx, env, evidence.OrderSubmitted, at, map[string]any{
		"client_order_id": req.ClientOrderID,
	})

	outcome, err := p.Execution.Submit(ctx, env, req)
	result.Outcome = &outcome
	result.Replayed = outcome.Replayed

	if errors.Is(err, execution.ErrEnvelopeReused) {
		// Not an ambiguous outcome: nothing was sent. The caller asked for a second
		// order under an envelope id that already has one, and the platform cannot
		// tell which of the two intentions it was meant to act on (spec section 12.2).
		result.Stage = StageExecution
		result.Code = "ENVELOPE_REUSED"
		result.Reason = err.Error()
		result.Accepted = false
		return p.observed(env, result)
	}

	if err != nil {
		// An unresolved outcome is not a failure of the order. It means the platform
		// does not know, which is a state an operator resolves (spec section 19).
		p.record(ctx, env, evidence.OrderUnknown, at, map[string]any{
			"client_order_id": req.ClientOrderID,
			"reason":          err.Error(),
		})
		result.Stage = StageExecution
		result.Code = "OUTCOME_UNKNOWN"
		result.Reason = err.Error()
		result.Accepted = false
		return result
	}

	// Usage is spent at submission, not at fill. A grant that caps an hour of
	// exposure caps what was committed, and an order standing open at a venue is
	// committed whether or not it has filled.
	p.spend(ctx, env, outcome, at)

	p.record(ctx, env, outcomeEvent(outcome.State), at, map[string]any{
		"client_order_id": outcome.ClientOrderID,
		"broker_order_id": outcome.BrokerOrderID,
		"state":           string(outcome.State),
		"filled_quantity": outcome.FilledQuantity,
	})

	result.Accepted = outcome.State != broker.StateRejected
	result.Code = string(outcome.State)
	return p.observed(env, result)
}

// observed hands a decided intent to the analytical plane.
//
// Every decided intent, not only the accepted ones. A fleet view built from
// executions alone cannot see a cohort that is being refused, and "forty agents all
// hit the same limit in the same minute" is exactly the signal the fleet engine
// exists to surface.
func (p *Pipeline) observed(env *intent.AgentExecutionEnvelope, r Result) Result {
	if p.Telemetry == nil || env == nil {
		return r
	}
	d := fleet.Decision{}
	if r.Authority != nil {
		d.AuthorityDecision = r.Authority.Code
	}
	if r.Policy != nil {
		d.PolicyAction = string(r.Policy.Action)
		d.PolicyBundleID = r.Policy.BundleID
	}
	p.Telemetry.Observe(env, d)
	return r
}

// priorOutcome looks for a resolved idempotency record.
//
// A store error is not treated as "no prior record": that would turn an outage into a
// duplicate execution. It returns not-found, and the claim inside execution.Service
// then fails closed for the same reason.
func (p *Pipeline) priorOutcome(ctx context.Context, env *intent.AgentExecutionEnvelope) (execution.Outcome, bool) {
	record, err := p.Execution.Store.Load(ctx, env.TenantID, env.IdempotencyKey)
	if err != nil || record == nil || record.State != execution.RecordResolved {
		return execution.Outcome{}, false
	}
	outcome := record.Outcome
	outcome.Replayed = true
	return outcome, true
}

// orderRequest translates a validated envelope into a venue-neutral order.
//
// The client order id is derived from the idempotency key, which is what makes the
// order findable after an ambiguous timeout. The Tradier adapter refuses identifiers
// its venue cannot carry, and this is where that refusal surfaces.
func (p *Pipeline) orderRequest(env *intent.AgentExecutionEnvelope) (broker.OrderRequest, error) {
	symbol, ok := p.Symbols.SymbolFor(env.Intent.InstrumentID)
	if !ok {
		return broker.OrderRequest{}, fmt.Errorf(
			"no venue symbol for instrument %s; a ticker is not a canonical identifier "+
				"and the platform will not guess one", env.Intent.InstrumentID)
	}

	return broker.OrderRequest{
		ClientOrderID: "coid-" + env.IdempotencyKey,
		TenantID:      env.TenantID,
		InstrumentID:  env.Intent.InstrumentID,
		Symbol:        symbol,
		AssetClass:    env.Intent.AssetClass,
		Side:          env.Intent.Side,
		OrderType:     env.Intent.OrderType,
		Quantity:      env.Intent.Quantity,
		Notional:      env.Intent.Notional,
		LimitPrice:    env.Intent.LimitPrice,
		StopPrice:     env.Intent.StopPrice,
		TimeInForce:   env.Intent.TimeInForce,
		ExtendedHours: env.Intent.ExtendedHours,
	}, nil
}

// spend records what this submission consumed of the grant.
//
// A failure to record is logged through the sink rather than failing the request,
// with a consequence worth stating: an unrecorded submission is a submission the next
// rolling-limit check does not see, so the ledger understates. It never overstates,
// which is the direction that would deny orders that were within the grant.
func (p *Pipeline) spend(ctx context.Context, env *intent.AgentExecutionEnvelope,
	outcome execution.Outcome, at time.Time) {

	if p.UsageRecorder == nil || outcome.Replayed {
		return
	}
	notional, determinable := authority.EffectiveNotional(env.Intent)
	if !determinable {
		// The grant would have denied an indeterminate notional if it capped size,
		// so reaching here means it does not. Recording zero is honest; recording a
		// guess would be inventing exposure.
		notional = 0
	}

	terminal := outcome.State == broker.StateFilled ||
		outcome.State == broker.StateRejected ||
		outcome.State == broker.StateCancelled

	if err := p.UsageRecorder.Record(ctx, authority.Entry{
		TenantID:       env.TenantID,
		GrantID:        env.AuthorityGrantID,
		IdempotencyKey: env.IdempotencyKey,
		Notional:       notional,
		SubmittedAt:    at,
		Open:           !terminal,
	}); err != nil {
		p.record(ctx, env, evidence.OrderUnknown, at, map[string]any{
			"usage_not_recorded": err.Error(),
		})
	}
}

func outcomeEvent(state broker.ExecutionState) evidence.EventName {
	switch state {
	case broker.StateFilled:
		return evidence.OrderFilled
	case broker.StateRejected:
		return evidence.OrderRejected
	case broker.StateCancelled:
		return evidence.OrderCancelled
	case broker.StateUnknown:
		return evidence.OrderUnknown
	default:
		return evidence.OrderAccepted
	}
}

func (p *Pipeline) deny(r Result, stage, code, reason string) Result {
	r.Accepted = false
	r.Stage = stage
	r.Code = code
	r.Reason = reason
	return p.observed(r.envelope, r)
}

func (p *Pipeline) refuse(stage, code string, err error, at time.Time, details []string) Result {
	result := Result{Stage: stage, Code: code, Reason: err.Error(), DecidedAt: at, Details: details}
	var verrs intent.ValidationErrors
	if errors.As(err, &verrs) {
		result.Details = verrs.Codes()
	}
	return result
}

// sequence gives evidence events a monotonic order within one submission.
var sequence struct {
	sync.Mutex
	n int64
}

func nextSequence() int64 {
	sequence.Lock()
	defer sequence.Unlock()
	sequence.n++
	return sequence.n
}

// record emits evidence, and never lets a failure to record fail a decision.
//
// Spec section 17: telemetry unavailable means buffer locally, not stop enforcing.
// The error is deliberately swallowed here and surfaces through the sink's own
// operational logging, because a decision that failed because the audit trail was
// down would be the audit trail causing the incident.
func (p *Pipeline) record(ctx context.Context, env *intent.AgentExecutionEnvelope,
	name evidence.EventName, at time.Time, payload map[string]any) {

	if p.Evidence == nil || env == nil {
		return
	}

	seq := nextSequence()
	_, _ = p.Evidence.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_%s_%d", env.EnvelopeID, name, seq),
		EventName:     name,
		TenantID:      env.TenantID,
		AggregateID:   env.EnvelopeID,
		CorrelationID: correlationOf(env),
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      seq,
		Payload:       payload,
	})
}

// correlationOf falls back to the envelope id.
//
// An envelope without a correlation id is valid: spec section 12.2 leaves it
// optional and the gateway assigns one. Using the envelope id keeps the chain
// walkable rather than leaving the event unplaceable.
func correlationOf(env *intent.AgentExecutionEnvelope) string {
	if env.CorrelationID != "" {
		return env.CorrelationID
	}
	return env.EnvelopeID
}
