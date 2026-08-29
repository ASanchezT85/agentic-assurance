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
	"agentic-assurance/internal/control"
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
	StageControl     = "CONTROL"
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

	// ControlID names the fleet control that refused, when one did. Kept on the
	// result so telemetry can record which control acted without re-deriving it from
	// the code.
	ControlID string

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

// ControlSource supplies the fleet controls in force for a tenant.
//
// Read-only, and an interface for that reason: the enforcement plane applies controls
// and never creates them. Creating one requires a customer authorization the
// intelligence plane cannot construct (INV-009).
type ControlSource interface {
	InForce(ctx context.Context, tenantID string, at time.Time) ([]control.Control, error)

	// Consume takes one slot in a THROTTLE's window. It writes, which is the single
	// exception to this interface being read-only: a rate limit cannot be enforced by
	// a component that cannot remember what it allowed.
	Consume(ctx context.Context, tenantID string, c control.Control,
		idempotencyKey string, at time.Time) (bool, int, error)
}

// EvidenceSink records what happened. It is deliberately fire-and-forget from the
// pipeline's point of view: spec section 17 requires production to continue when
// telemetry is unavailable, so a failure to record must never fail a decision.
type EvidenceSink interface {
	Append(ctx context.Context, e evidence.Event) (bool, error)

	// AppendBatch writes a submission's events in one transaction. Written one at a
	// time they were six transactions per intent, which measured at about 95 ms of
	// the 120 ms an accepted intent took — six times the cost of the decision they
	// describe, for an enforcement computation of 12.5 microseconds.
	AppendBatch(ctx context.Context, events []evidence.Event) error
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

	// Controls are the fleet controls a customer authorized. Optional: without a
	// store the pipeline enforces everything else, and no fleet control binds —
	// which is exactly shadow mode, the state the platform was in until POST
	// /v1/controls existed.
	Controls ControlSource

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

	// Every path out of this function flushes, including the refusals: an intent that
	// was refused is exactly the one someone will ask about later.
	buffered := &recorder{}
	ctx = withRecorder(ctx, buffered)
	defer p.flush(ctx, buffered)

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
	if reused, by := p.keyClaimedByAnother(ctx, env); reused {
		// Refused rather than answered with the earlier order. A caller that reused a
		// key for a different intent would otherwise be told its order was accepted
		// and filled, and no such order was ever sent.
		p.record(ctx, env, evidence.IdentityFailed, at, map[string]any{
			"idempotency_key": env.IdempotencyKey,
			"claimed_by":      by,
			"reason":          "the idempotency key belongs to a different envelope",
		})
		return p.deny(result, StageIdempotency, "IDEMPOTENCY_KEY_REUSED",
			"this idempotency key was claimed by envelope "+by+"; a key identifies one "+
				"intent, and returning that one's outcome would report an order this "+
				"request never placed (spec section 12.2)")
	}

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

	// 6b. Fleet controls the customer authorized.
	//
	// After authority and before policy: a control is a customer decision about a
	// cohort or an account, which is broader than one order's ceiling and narrower
	// than the standing rulebook. An order the grant already refused never needs to
	// consult one.
	if p.Controls != nil {
		inForce, err := p.Controls.InForce(ctx, env.TenantID, at)
		if err != nil {
			// An unreadable control store denies, for the same reason an unreadable
			// grant store does: a control is in force until someone revokes it, and
			// treating "cannot read" as "none apply" would unenforce every one of
			// them exactly when the database is unhealthy.
			return p.deny(result, StageControl, "CONTROL_UNAVAILABLE",
				"fleet controls could not be read: "+err.Error())
		}
		if d := control.Evaluate(inForce, env, at); !d.Allowed {
			result.ControlID = d.ControlID
			// control.enforced, not control.applied: applying is what the customer
			// did once, enforcing is what the platform does on every order after.
			// Recording this as an application made the incident timeline report a
			// human action for each order the control stopped.
			p.record(ctx, env, evidence.ControlEnforced, at, map[string]any{
				"control":    d.Code,
				"control_id": d.ControlID,
				"reason":     d.Reason,
			})
			return p.deny(result, StageControl, d.Code, d.Reason)
		}

		// Throttles last among the controls, because a scope that is stopped outright
		// should be told that rather than told it is going too fast, and because a slot
		// consumed by an order some other control was going to refuse is a slot spent
		// on nothing.
		//
		// The slot is spent here rather than after the venue accepts. An order that
		// passes this stage and is then refused by policy still counted, which errs
		// toward throttling more — and for a control authorized during an incident,
		// that is the direction to err in.
		for _, throttle := range control.Throttles(inForce, env, at) {
			allowed, used, err := p.Controls.Consume(ctx, env.TenantID, throttle,
				env.IdempotencyKey, at)
			if err != nil {
				return p.deny(result, StageControl, "CONTROL_UNAVAILABLE",
					"a throttle could not be counted: "+err.Error())
			}
			if !allowed {
				d := control.Throttled(throttle, used)
				result.ControlID = d.ControlID
				p.record(ctx, env, evidence.ControlEnforced, at, map[string]any{
					"control":    d.Code,
					"control_id": d.ControlID,
					"reason":     d.Reason,
					"used":       used,
					"max_orders": throttle.MaxOrders,
					"window":     throttle.Window.String(),
				})
				return p.deny(result, StageControl, d.Code, d.Reason)
			}
		}
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
	// A control refusal already reaches the analytical plane as unauthorized flow,
	// because an intent counts as authorized only when authority and policy both
	// allowed. What was missing is which control did it, and the codes are different
	// operational stories: throttled is not isolated is not read-only.
	if r.Stage == StageControl {
		d.ControlDecision = r.Code
		d.ControlID = r.ControlID
	}
	p.Telemetry.Observe(env, d)
	return r
}

// keyClaimedByAnother reports whether this key already belongs to a different envelope.
//
// Checked before the replay path rather than inside it: a replay returns the earlier
// outcome, and the whole question here is whether that outcome belongs to this caller.
// A store error answers "no" and the claim inside execution fails closed for the same
// reason priorOutcome does — an outage must not become a refusal of legitimate retries.
func (p *Pipeline) keyClaimedByAnother(ctx context.Context, env *intent.AgentExecutionEnvelope) (bool, string) {
	record, err := p.Execution.Store.Load(ctx, env.TenantID, env.IdempotencyKey)
	if err != nil || record == nil || record.EnvelopeID == "" {
		return false, ""
	}
	if record.EnvelopeID == env.EnvelopeID {
		return false, ""
	}
	return true, record.EnvelopeID
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
	event := evidence.Event{
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
	}

	// Buffered for this submission and written once, at the end. Six events written
	// one at a time were six transactions on the hot path; the decision they describe
	// takes microseconds. A crash before the flush loses the account of a decision
	// that never returned — which is the same window the caller already has, and the
	// idempotency record that protects execution is still written synchronously.
	if buffered := recorderFrom(ctx); buffered != nil {
		buffered.add(event)
		return
	}
	_, _ = p.Evidence.Append(ctx, event)
}

// recorder buffers one submission's evidence.
type recorder struct {
	events []evidence.Event
}

type recorderKey struct{}

func withRecorder(ctx context.Context, r *recorder) context.Context {
	return context.WithValue(ctx, recorderKey{}, r)
}

func recorderFrom(ctx context.Context) *recorder {
	r, _ := ctx.Value(recorderKey{}).(*recorder)
	return r
}

func (r *recorder) add(e evidence.Event) { r.events = append(r.events, e) }

// flush writes what the submission recorded. Failure costs the audit trail and never
// the decision (spec section 17), so it is logged by the store and dropped here.
func (p *Pipeline) flush(ctx context.Context, r *recorder) {
	if p.Evidence == nil || r == nil || len(r.events) == 0 {
		return
	}
	_ = p.Evidence.AppendBatch(ctx, r.events)
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
