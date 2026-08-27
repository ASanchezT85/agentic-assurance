// Package broker defines the boundary between the platform and any execution venue.
//
// ADR-012: the core depends on this abstraction and never on a specific broker's SDK
// types. A field that exists because one broker happens to return it does not belong
// here; it belongs in that broker's adapter.
package broker

import (
	"context"
	"errors"
	"time"

	"agentic-assurance/internal/intent"
)

// ExecutionState is the canonical order state.
//
// UNKNOWN is the state that matters. Every other state is a fact; UNKNOWN is an
// admission that we do not have one, and the whole point of spec section 19 is that
// the system says so instead of guessing (INV-004).
type ExecutionState string

const (
	// StateUnknown means the platform does not know whether the broker received or
	// acted on the order. It is never a synonym for "failed" and never a licence to
	// resubmit.
	StateUnknown         ExecutionState = "UNKNOWN"
	StateAccepted        ExecutionState = "ACCEPTED"
	StateRejected        ExecutionState = "REJECTED"
	StatePartiallyFilled ExecutionState = "PARTIALLY_FILLED"
	StateFilled          ExecutionState = "FILLED"
	StateCancelled       ExecutionState = "CANCELLED"
	StateExpired         ExecutionState = "EXPIRED"
)

// Terminal reports whether a state can still change at the broker.
//
// UNKNOWN is deliberately not terminal: it is the state that must be resolved by
// reconciliation rather than closed by assumption.
func (s ExecutionState) Terminal() bool {
	switch s {
	case StateFilled, StateRejected, StateCancelled, StateExpired:
		return true
	}
	return false
}

// Fill is one execution against an order.
type Fill struct {
	FillID     string
	Quantity   float64
	Price      float64
	OccurredAt time.Time
}

// BrokerOrder is the canonical order record. It is ours, not the broker's.
type BrokerOrder struct {
	// ClientOrderID is the platform's identifier, derived from the envelope's
	// idempotency key. It is what makes reconciliation possible: after an ambiguous
	// timeout it is the only handle we have on an order the broker may or may not
	// have created.
	ClientOrderID string

	// BrokerOrderID is the venue's identifier. Empty until the venue confirms.
	BrokerOrderID string

	State            ExecutionState
	FilledQuantity   float64
	AverageFillPrice float64
	Fills            []Fill

	SubmittedAt time.Time
	UpdatedAt   time.Time

	// RejectReason carries the venue's explanation, unparsed. The core does not
	// interpret broker-specific reason codes.
	RejectReason string
}

// OrderRequest is a normalized submission. It carries no broker-specific fields.
type OrderRequest struct {
	ClientOrderID string
	TenantID      string
	InstrumentID  string

	// Symbol is venue metadata, not identity. The core keys on InstrumentID
	// (spec section 13); an adapter needs a symbol to talk to its venue and is the
	// only place allowed to care.
	Symbol string

	AssetClass    intent.AssetClass
	Side          intent.Side
	OrderType     intent.OrderType
	Quantity      *float64
	Notional      *float64
	LimitPrice    *float64
	StopPrice     *float64
	TimeInForce   intent.TimeInForce
	ExtendedHours bool
}

// Account is the venue's view of an account.
type Account struct {
	AccountID   string
	Currency    string
	Cash        float64
	BuyingPower float64
	// PaperTrading is not decoration. V0 implements no real-money path, and an
	// adapter that cannot assert this is one nobody should connect.
	PaperTrading bool
}

// Position is one held instrument.
type Position struct {
	InstrumentID string
	Symbol       string
	Quantity     float64
	AveragePrice float64
}

// Capabilities describes what a venue supports, so the core can refuse an order the
// venue cannot express rather than discovering it at submission.
type Capabilities struct {
	Name                  string
	PaperOnly             bool
	AssetClasses          []intent.AssetClass
	OrderTypes            []intent.OrderType
	SupportsNotional      bool
	SupportsExtendedHours bool
	// SupportsClientOrderID is required, not optional. An adapter that cannot echo
	// our identifier back cannot be reconciled after a timeout, which makes INV-004
	// unenforceable against it.
	SupportsClientOrderID bool
}

// ErrTimeout is returned when the platform did not get an answer.
//
// It is a distinct error because it is the only one that must never be treated as a
// failure: a timeout means the outcome is unknown, and an unknown outcome that gets
// retried is how one intent becomes two orders (INV-004).
var ErrTimeout = errors.New("broker did not respond in time; outcome is unknown")

// ErrOrderNotFound is returned by lookups when the venue has no such order.
var ErrOrderNotFound = errors.New("broker has no such order")

// ErrUnsupported is returned when the venue cannot express the request.
var ErrUnsupported = errors.New("broker does not support this request")

// Adapter is the contract every venue implements (spec section 18).
//
// Implementations must be safe for concurrent use, and must not retry on their own:
// deciding what to do about an ambiguous outcome is the caller's job, and an adapter
// that quietly retries removes the caller's ability to make that decision.
type Adapter interface {
	Capabilities() Capabilities

	// SubmitOrder sends an order. On ErrTimeout the caller must treat the outcome
	// as UNKNOWN and reconcile; it must never resubmit.
	SubmitOrder(ctx context.Context, req OrderRequest) (BrokerOrder, error)

	CancelOrder(ctx context.Context, clientOrderID string) error

	// GetOrder looks up by our client order id, which is what makes an order
	// findable after a timeout swallowed the broker's identifier.
	GetOrder(ctx context.Context, clientOrderID string) (BrokerOrder, error)

	GetOrders(ctx context.Context, since time.Time) ([]BrokerOrder, error)
	GetPositions(ctx context.Context) ([]Position, error)
	GetAccount(ctx context.Context) (Account, error)

	// Reconcile answers the only question that matters after an ambiguous timeout:
	// does this order exist at the venue, and in what state.
	Reconcile(ctx context.Context, clientOrderID string) (BrokerOrder, error)
}
