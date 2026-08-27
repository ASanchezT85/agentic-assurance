package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

// Service submits orders exactly once and resolves ambiguity by asking, never by
// assuming.
//
// There is no retry knob, no maximum-attempts setting, and no resubmit method. That
// is the design: spec section 19 says an unresolved ambiguous outcome remains UNKNOWN
// and goes to an operator, so the ability to resubmit is simply absent rather than
// disabled by configuration someone could change at 3am.
type Service struct {
	Broker broker.Adapter
	Store  Store

	// Cache is optional. A nil cache means every read goes to the store, which is
	// slower and equally correct (ADR-015, INV-011).
	Cache Cache

	// Now is injectable so timeouts and reconciliation are testable.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ErrUnresolved is returned when an order's outcome could not be established.
//
// It is not a failure of the order. It means the platform does not know, which is a
// state an operator resolves and code must not paper over.
var ErrUnresolved = errors.New("order outcome is unknown and could not be reconciled")

// Submit executes an authorized envelope at most once.
//
// The flow, in the order it happens:
//
//  1. a resolved record, from cache or store, returns the prior outcome
//  2. a pending record means a previous attempt died mid-flight: reconcile
//  3. otherwise claim the key, then submit exactly once
//  4. a timeout makes the outcome UNKNOWN: reconcile, never resubmit
func (s *Service) Submit(ctx context.Context, env *intent.AgentExecutionEnvelope, req broker.OrderRequest) (Outcome, error) {
	if env == nil {
		return Outcome{}, fmt.Errorf("no envelope")
	}
	if env.TenantID == "" || env.IdempotencyKey == "" {
		return Outcome{}, fmt.Errorf("tenant and idempotency key are required")
	}
	if req.ClientOrderID == "" {
		return Outcome{}, fmt.Errorf("client order id is required; without one an order cannot be reconciled")
	}

	if cached := s.fromCache(ctx, env.TenantID, env.IdempotencyKey); cached != nil {
		return replay(cached.Outcome), nil
	}

	now := s.now()
	existing, claimed, err := s.Store.Claim(ctx, Record{
		TenantID:       env.TenantID,
		IdempotencyKey: env.IdempotencyKey,
		EnvelopeID:     env.EnvelopeID,
		ClientOrderID:  req.ClientOrderID,
		State:          RecordPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		// Spec section 17: the idempotency store is authoritative control state,
		// and an unreadable one cannot be overridden by trying anyway.
		return Outcome{}, fmt.Errorf("idempotency store unavailable: %w", err)
	}

	if !claimed {
		return s.resumeExisting(ctx, existing)
	}

	return s.submitOnce(ctx, env, req)
}

// resumeExisting handles a key that was already claimed.
func (s *Service) resumeExisting(ctx context.Context, existing *Record) (Outcome, error) {
	if existing == nil {
		return Outcome{}, fmt.Errorf("store reported an existing record but returned none")
	}

	if existing.State == RecordResolved {
		s.cache(ctx, *existing)
		return replay(existing.Outcome), nil
	}

	// PENDING. A previous attempt claimed this key and never finished. Whether it
	// reached the venue is exactly what reconciliation is for. Submitting again
	// here is the bug INV-004 exists to prevent.
	return s.reconcileAndResolve(ctx, existing.TenantID, existing.IdempotencyKey, existing.ClientOrderID)
}

// submitOnce sends the order. It is called at most once per idempotency key, and
// contains no loop.
func (s *Service) submitOnce(ctx context.Context, env *intent.AgentExecutionEnvelope, req broker.OrderRequest) (Outcome, error) {
	order, err := s.Broker.SubmitOrder(ctx, req)

	if err != nil {
		if errors.Is(err, broker.ErrTimeout) {
			// The outcome is unknown. Not failed, not succeeded: unknown.
			return s.reconcileAndResolve(ctx, env.TenantID, env.IdempotencyKey, req.ClientOrderID)
		}
		// A refusal the venue actually gave us is a fact, and a final one.
		outcome := Outcome{
			State:         broker.StateRejected,
			ClientOrderID: req.ClientOrderID,
			RejectReason:  err.Error(),
		}
		if resolveErr := s.resolve(ctx, env.TenantID, env.IdempotencyKey, outcome); resolveErr != nil {
			return outcome, resolveErr
		}
		return outcome, nil
	}

	outcome, err := outcomeFrom(req.ClientOrderID, order)
	if err != nil {
		// INV-012: a broker adapter returning nonsense must not write nonsense into
		// the canonical record. The order becomes UNKNOWN and is reconciled like
		// any other ambiguity.
		return s.reconcileAndResolve(ctx, env.TenantID, env.IdempotencyKey, req.ClientOrderID)
	}

	if err := s.resolve(ctx, env.TenantID, env.IdempotencyKey, outcome); err != nil {
		return outcome, err
	}
	return outcome, nil
}

// reconcileAndResolve asks the venue what happened, once.
//
// There is no retry loop. If the venue cannot answer, the record stays PENDING and
// the caller gets ErrUnresolved, which is the operator flow of spec section 19.
func (s *Service) reconcileAndResolve(ctx context.Context, tenantID, key, clientOrderID string) (Outcome, error) {
	unknown := Outcome{State: broker.StateUnknown, ClientOrderID: clientOrderID}

	order, err := s.Broker.Reconcile(ctx, clientOrderID)
	switch {
	case err == nil:
		outcome, convErr := outcomeFrom(clientOrderID, order)
		if convErr != nil {
			return unknown, fmt.Errorf("%w: broker returned an unusable order: %v", ErrUnresolved, convErr)
		}
		if resolveErr := s.resolve(ctx, tenantID, key, outcome); resolveErr != nil {
			return outcome, resolveErr
		}
		return outcome, nil

	case errors.Is(err, broker.ErrOrderNotFound):
		// The venue has no such order. That is not permission to submit again:
		// "not found" and "not yet visible" are indistinguishable from here, and
		// spec section 19 sends an unresolved outcome to an operator rather than
		// letting the platform guess (INV-004).
		return unknown, fmt.Errorf("%w: the venue reports no order for %s, which does not "+
			"establish that none was created", ErrUnresolved, clientOrderID)

	default:
		return unknown, fmt.Errorf("%w: reconciliation failed for %s: %v", ErrUnresolved, clientOrderID, err)
	}
}

func (s *Service) resolve(ctx context.Context, tenantID, key string, o Outcome) error {
	if err := s.Store.Resolve(ctx, tenantID, key, o, s.now()); err != nil {
		return fmt.Errorf("idempotency record not resolved: %w", err)
	}
	s.cache(ctx, Record{
		TenantID:       tenantID,
		IdempotencyKey: key,
		ClientOrderID:  o.ClientOrderID,
		State:          RecordResolved,
		Outcome:        o,
		UpdatedAt:      s.now(),
	})
	return nil
}

func (s *Service) fromCache(ctx context.Context, tenantID, key string) *Record {
	if s.Cache == nil {
		return nil
	}
	rec, ok := s.Cache.Get(ctx, tenantID, key)
	if !ok || rec == nil || rec.State != RecordResolved {
		// A cache miss is not evidence that a request is new, and a cached PENDING
		// is not something to act on. Either way the store decides (ADR-015).
		return nil
	}
	return rec
}

func (s *Service) cache(ctx context.Context, rec Record) {
	if s.Cache != nil {
		s.Cache.Put(ctx, rec)
	}
}

func replay(o Outcome) Outcome {
	o.Replayed = true
	return o
}

// outcomeFrom converts a venue's order into a canonical outcome, refusing anything
// that would corrupt the core (INV-012).
func outcomeFrom(expectedClientOrderID string, order broker.BrokerOrder) (Outcome, error) {
	switch {
	case order.ClientOrderID != expectedClientOrderID:
		return Outcome{}, fmt.Errorf("broker returned client order id %q, expected %q",
			order.ClientOrderID, expectedClientOrderID)
	case order.State == "":
		return Outcome{}, fmt.Errorf("broker returned an order with no state")
	case !knownState(order.State):
		return Outcome{}, fmt.Errorf("broker returned unknown state %q", order.State)
	case order.FilledQuantity < 0:
		return Outcome{}, fmt.Errorf("broker reported a negative filled quantity")
	}

	return Outcome{
		State:          order.State,
		ClientOrderID:  order.ClientOrderID,
		BrokerOrderID:  order.BrokerOrderID,
		FilledQuantity: order.FilledQuantity,
		RejectReason:   order.RejectReason,
	}, nil
}

func knownState(s broker.ExecutionState) bool {
	switch s {
	case broker.StateUnknown, broker.StateAccepted, broker.StateRejected,
		broker.StatePartiallyFilled, broker.StateFilled, broker.StateCancelled,
		broker.StateExpired:
		return true
	}
	return false
}
