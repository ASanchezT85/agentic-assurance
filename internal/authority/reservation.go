package authority

import (
	"context"
	"fmt"
	"time"

	"agentic-assurance/internal/money"
)

// Atomic reservation of a grant's mutable limits.
//
// The check and the record used to be two transactions with a venue call between them:
// authority read consumed usage, the pipeline submitted, and only then was usage
// written. Every operation was race-free and the invariant was not. Two intents of
// 4,000 against a 10,000 rolling ceiling both read zero, both passed, and four of them
// put 16,000 through — reproduced against real PostgreSQL before this existed.
//
// INV-002 is a property of the system rather than of the ledger. A data structure that
// never loses a concurrent write is not a limit; a limit is a decision nobody else can
// make at the same time.
//
// So the reservation is the authorization. Reserve counts, decides and writes inside
// one transaction, serialized per grant, and nothing reaches a venue without holding
// one.

// ReservationState is where a reserved slot ended up.
type ReservationState string

const (
	// StateReserved is capacity held for an intent whose outcome is not yet known.
	// It counts against every limit, because the order may be live at a venue.
	StateReserved ReservationState = "RESERVED"

	// StateCommitted is capacity spent by an order the venue accepted. It counts
	// against rolling and daily notional for as long as the window holds it.
	StateCommitted ReservationState = "COMMITTED"

	// StateReleased is capacity returned. A definite venue rejection releases: the
	// order does not exist, and leaving it consumed would let anyone exhaust a
	// customer's grant with requests a venue was always going to refuse.
	//
	// An ambiguous outcome is never released. It stays RESERVED until reconciliation
	// says what happened, because the alternative is releasing capacity for an order
	// that may be working (INV-004).
	StateReleased ReservationState = "RELEASED"
)

// ReservationIdentity is what a reservation was taken for.
//
// A reservation used to be keyed by (tenant, idempotency_key) and nothing else, so a
// repeated key returned ALLOW without asking what it had been reserved for. A key left
// behind by a failure that never reached a venue could then authorize a different
// envelope, a different grant and a different amount — the ledger describing an intent
// that never executed while the one that did was invisible to it.
//
// These fields are the immutable identity of an economic request. A repeated key is a
// retry only if all of them match; anything else is a different intent wearing the same
// key, and it is refused rather than allowed.
type ReservationIdentity struct {
	EnvelopeID  string
	PrincipalID string
	AccountID   string
}

// Reserver holds capacity atomically.
//
// Separate from Recorder because these are different acts: Record wrote down what had
// already happened, Reserve decides whether it may. A component that could do the first
// without the second is how the ceiling got exceeded.
type Reserver interface {
	// Reserve holds capacity for one intent, or refuses. The decision it returns is
	// the authoritative one; the code matches what Evaluate would have said, because
	// an operator reading ROLLING_LIMIT_EXCEEDED should not care which check produced
	// it.
	//
	// Reserving twice under one idempotency key is not two reservations. A retry that
	// already holds capacity keeps it: the ledger is keyed by the key for the same
	// reason the idempotency record is.
	Reserve(ctx context.Context, g *Grant, idempotencyKey string, notional money.Amount,
		who ReservationIdentity, at time.Time) (Decision, error)

	// Release returns capacity when it is known that nothing was sent.
	//
	// Distinct from Settle, which records what a venue did. A pre-venue failure —
	// the decision receipt could not commit, the envelope was already used, the order
	// could not be built — establishes that no order exists, and leaving capacity
	// held for one would let a broken caller exhaust a grant without ever trading.
	//
	// An ambiguous outcome is never released here. That is the one case where holding
	// is right, because the order may be working.
	Release(ctx context.Context, tenantID, idempotencyKey string, at time.Time) error

	// Settle records what the venue did. Terminal outcomes close the open-order count;
	// a definite rejection releases the notional; an unknown outcome changes nothing
	// and leaves the capacity held.
	Settle(ctx context.Context, tenantID, idempotencyKey string, state ReservationState,
		open bool, at time.Time) error
}

// reservationDecision builds the same shape Evaluate returns, so a refusal from the
// atomic path is indistinguishable to a caller from a refusal by the cheap pre-check.
func reservationDecision(g *Grant, at time.Time, allowed bool, code, reason string) Decision {
	if allowed {
		return allow(g, at)
	}
	return deny(g, at, code, reason)
}

// checkLimits is the arithmetic, shared by the pre-check in Evaluate and by the atomic
// reservation, so the two can never disagree about what a limit means.
func checkLimits(limits Limits, consumed Snapshot, notional money.Amount) (code, reason string) {
	if limits.Rolling1hNotional > 0 && consumed.Rolling1hNotional.Add(notional) > limits.Rolling1hNotional {
		return "ROLLING_LIMIT_EXCEEDED", fmt.Sprintf(
			"%s already used in the last hour plus %s exceeds the rolling limit %s",
			consumed.Rolling1hNotional, notional, limits.Rolling1hNotional)
	}
	if limits.DailyNotional > 0 && consumed.DailyNotional.Add(notional) > limits.DailyNotional {
		return "DAILY_LIMIT_EXCEEDED", fmt.Sprintf(
			"%s already used today plus %s exceeds the daily limit %s",
			consumed.DailyNotional, notional, limits.DailyNotional)
	}
	if limits.MaxOpenOrders > 0 && consumed.OpenOrders >= limits.MaxOpenOrders {
		return "MAX_OPEN_ORDERS_EXCEEDED", fmt.Sprintf(
			"%d orders already open, limit is %d", consumed.OpenOrders, limits.MaxOpenOrders)
	}
	return "", ""
}
