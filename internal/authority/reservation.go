package authority

import (
	"context"
	"fmt"
	"time"
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
	Reserve(ctx context.Context, g *Grant, idempotencyKey string, notional float64,
		at time.Time) (Decision, error)

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
func checkLimits(limits Limits, consumed Snapshot, notional float64) (code, reason string) {
	if limits.Rolling1hNotional > 0 && consumed.Rolling1hNotional+notional > limits.Rolling1hNotional {
		return "ROLLING_LIMIT_EXCEEDED", fmt.Sprintf(
			"%.2f already used in the last hour plus %.2f exceeds the rolling limit %.2f",
			consumed.Rolling1hNotional, notional, limits.Rolling1hNotional)
	}
	if limits.DailyNotional > 0 && consumed.DailyNotional+notional > limits.DailyNotional {
		return "DAILY_LIMIT_EXCEEDED", fmt.Sprintf(
			"%.2f already used today plus %.2f exceeds the daily limit %.2f",
			consumed.DailyNotional, notional, limits.DailyNotional)
	}
	if limits.MaxOpenOrders > 0 && consumed.OpenOrders >= limits.MaxOpenOrders {
		return "MAX_OPEN_ORDERS_EXCEEDED", fmt.Sprintf(
			"%d orders already open, limit is %d", consumed.OpenOrders, limits.MaxOpenOrders)
	}
	return "", ""
}
