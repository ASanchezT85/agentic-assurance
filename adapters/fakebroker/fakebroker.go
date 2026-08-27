// Package fakebroker is a deterministic in-memory venue.
//
// It exists to make the failure matrix of spec section 54 reproducible. A real venue
// cannot be asked to time out after receiving an order, and that is precisely the
// case the platform must survive, so the fake can be told to do it.
package fakebroker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

// Fault is an injected failure. Each one corresponds to a row of section 54.
type Fault string

const (
	FaultNone Fault = ""

	// FaultTimeoutBeforeReceipt: the request never reached the venue. No order
	// exists, and reconciliation will find nothing.
	FaultTimeoutBeforeReceipt Fault = "TIMEOUT_BEFORE_RECEIPT"

	// FaultTimeoutAfterReceipt: the venue accepted the order and the response was
	// lost. The order exists. This is the case that turns one intent into two
	// orders if the platform retries.
	FaultTimeoutAfterReceipt Fault = "TIMEOUT_AFTER_RECEIPT"

	// FaultReject: the venue refused the order outright.
	FaultReject Fault = "REJECT"

	// FaultPartialFill: the order fills partway and stays open.
	FaultPartialFill Fault = "PARTIAL_FILL"

	// FaultFillAfterLocalTimeout: the response was lost and the order then filled.
	FaultFillAfterLocalTimeout Fault = "FILL_AFTER_LOCAL_TIMEOUT"

	// FaultStaleStatus: lookups report a state the venue has already moved past.
	FaultStaleStatus Fault = "STALE_STATUS"

	// FaultUnreconcilable: the venue took the order, lost the response, and cannot
	// answer lookups either. The worst realistic case: the order may exist and
	// there is no way to find out, so it stays UNKNOWN and goes to an operator.
	FaultUnreconcilable Fault = "UNRECONCILABLE"

	// FaultCancelRace: a cancel arrives after the order has already filled.
	FaultCancelRace Fault = "CANCEL_RACE"
)

// Broker is a fake venue. The zero value is not usable; call New.
type Broker struct {
	mu sync.Mutex

	// orders is keyed by client order id, which is how a real venue must also be
	// able to find an order for reconciliation to work at all.
	orders map[string]*broker.BrokerOrder

	// faults are keyed by client order id and consumed on use, so a test can make
	// one specific submission fail without arming a global switch.
	faults map[string]Fault

	// submissions counts every call that reached the venue, including the ones
	// whose response was lost. It is what a duplicate-execution test asserts on:
	// counting orders would miss a second submission the venue deduplicated.
	submissions map[string]int

	now func() time.Time
	seq int
}

func New() *Broker {
	return &Broker{
		orders:      map[string]*broker.BrokerOrder{},
		faults:      map[string]Fault{},
		submissions: map[string]int{},
		now:         time.Now,
	}
}

// SetClock makes fill timestamps deterministic.
func (b *Broker) SetClock(fn func() time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.now = fn
}

// InjectFault arms a fault for one client order id.
func (b *Broker) InjectFault(clientOrderID string, f Fault) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.faults[clientOrderID] = f
}

// Submissions reports how many times a client order id reached the venue.
//
// This is the number INV-004 asserts on. An order count would hide a second
// submission that the venue happened to reject as a duplicate; what the invariant
// forbids is the platform sending it at all.
func (b *Broker) Submissions(clientOrderID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.submissions[clientOrderID]
}

func (b *Broker) Capabilities() broker.Capabilities {
	return broker.Capabilities{
		Name:                  "fakebroker",
		PaperOnly:             true,
		AssetClasses:          []intent.AssetClass{intent.AssetEquity, intent.AssetETF},
		OrderTypes:            []intent.OrderType{intent.OrderMarket, intent.OrderLimit, intent.OrderStop, intent.OrderStopLimit},
		SupportsNotional:      true,
		SupportsExtendedHours: true,
		SupportsClientOrderID: true,
	}
}

func (b *Broker) SubmitOrder(ctx context.Context, req broker.OrderRequest) (broker.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return broker.BrokerOrder{}, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if req.ClientOrderID == "" {
		return broker.BrokerOrder{}, fmt.Errorf("%w: client order id is required", broker.ErrUnsupported)
	}

	// Count the attempt before anything else. A submission that times out still
	// happened, and that is the whole point of this counter.
	b.submissions[req.ClientOrderID]++

	fault := b.faults[req.ClientOrderID]

	// A venue that honours client order ids does not create the same order twice.
	// The platform must not rely on this: INV-004 is about not sending a duplicate,
	// not about the venue catching one. It is modelled because real venues do it,
	// and a test that pretends otherwise would be testing a fiction.
	if existing, ok := b.orders[req.ClientOrderID]; ok {
		if fault == FaultTimeoutAfterReceipt || fault == FaultTimeoutBeforeReceipt {
			return broker.BrokerOrder{}, broker.ErrTimeout
		}
		return *existing, nil
	}

	if fault == FaultTimeoutBeforeReceipt {
		// Never reached the venue: no order is created.
		return broker.BrokerOrder{}, broker.ErrTimeout
	}

	b.seq++
	order := &broker.BrokerOrder{
		ClientOrderID: req.ClientOrderID,
		BrokerOrderID: fmt.Sprintf("fake-%06d", b.seq),
		State:         broker.StateAccepted,
		SubmittedAt:   b.now().UTC(),
		UpdatedAt:     b.now().UTC(),
	}

	switch fault {
	case FaultReject:
		order.State = broker.StateRejected
		order.RejectReason = "insufficient buying power"
	case FaultPartialFill:
		order.State = broker.StatePartiallyFilled
		b.applyFill(order, req, 0.4)
	case FaultFillAfterLocalTimeout:
		order.State = broker.StateFilled
		b.applyFill(order, req, 1.0)
	}

	b.orders[req.ClientOrderID] = order

	if fault == FaultTimeoutAfterReceipt || fault == FaultFillAfterLocalTimeout ||
		fault == FaultUnreconcilable {
		// The venue acted; the answer was lost in transit.
		return broker.BrokerOrder{}, broker.ErrTimeout
	}
	return *order, nil
}

func (b *Broker) applyFill(order *broker.BrokerOrder, req broker.OrderRequest, fraction float64) {
	qty := 100.0
	if req.Quantity != nil {
		qty = *req.Quantity
	}
	filled := qty * fraction
	price := 100.0
	if req.LimitPrice != nil {
		price = *req.LimitPrice
	}
	order.FilledQuantity = filled
	order.AverageFillPrice = price
	order.Fills = append(order.Fills, broker.Fill{
		FillID:     fmt.Sprintf("fill-%s-%d", order.ClientOrderID, len(order.Fills)+1),
		Quantity:   filled,
		Price:      price,
		OccurredAt: b.now().UTC(),
	})
}

func (b *Broker) GetOrder(ctx context.Context, clientOrderID string) (broker.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return broker.BrokerOrder{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.faults[clientOrderID] {
	case FaultUnreconcilable:
		return broker.BrokerOrder{}, broker.ErrTimeout
	}

	order, ok := b.orders[clientOrderID]
	if !ok {
		return broker.BrokerOrder{}, broker.ErrOrderNotFound
	}

	if b.faults[clientOrderID] == FaultStaleStatus {
		stale := *order
		stale.State = broker.StateAccepted
		stale.FilledQuantity = 0
		stale.Fills = nil
		return stale, nil
	}
	return *order, nil
}

// Reconcile answers whether an order exists at the venue and in what state.
func (b *Broker) Reconcile(ctx context.Context, clientOrderID string) (broker.BrokerOrder, error) {
	return b.GetOrder(ctx, clientOrderID)
}

func (b *Broker) CancelOrder(ctx context.Context, clientOrderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	order, ok := b.orders[clientOrderID]
	if !ok {
		return broker.ErrOrderNotFound
	}
	if b.faults[clientOrderID] == FaultCancelRace {
		order.State = broker.StateFilled
		order.UpdatedAt = b.now().UTC()
		return fmt.Errorf("%w: order already filled", broker.ErrUnsupported)
	}
	if order.State.Terminal() {
		return fmt.Errorf("%w: order is %s", broker.ErrUnsupported, order.State)
	}
	order.State = broker.StateCancelled
	order.UpdatedAt = b.now().UTC()
	return nil
}

func (b *Broker) GetOrders(ctx context.Context, since time.Time) ([]broker.BrokerOrder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []broker.BrokerOrder
	for _, o := range b.orders {
		if !o.SubmittedAt.Before(since) {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (b *Broker) GetPositions(ctx context.Context) ([]broker.Position, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *Broker) GetAccount(ctx context.Context) (broker.Account, error) {
	if err := ctx.Err(); err != nil {
		return broker.Account{}, err
	}
	return broker.Account{
		AccountID:    "fake-account",
		Currency:     "USD",
		Cash:         1000000,
		BuyingPower:  1000000,
		PaperTrading: true,
	}, nil
}
