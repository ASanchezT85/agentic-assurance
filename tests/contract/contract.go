// Package contract is the behavioural definition of broker.Adapter.
//
// Spec section 18 says a second adapter "or contract test implementation" must exist
// before the architecture is considered stable. This is that implementation, and it
// is the more useful half.
//
// An interface says what an adapter must have. It cannot say that a timeout means
// UNKNOWN rather than failed, that a lookup by our identifier must find an order the
// venue created under it, or that an unmapped status must error rather than default
// to something plausible. Those are the rules the core actually depends on, and until
// now they lived in the comments on internal/broker/adapter.go and in three separate
// test files that could each be right about a different contract.
//
// Any adapter passes or does not pass. Running it against a new venue is how a
// reviewer finds out whether the abstraction fits before the code depends on it.
package contract

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

func f(v float64) *float64 { return &v }

// Subject is an adapter under test plus what a harness needs to drive it.
type Subject struct {
	Name    string
	Adapter broker.Adapter

	// Arm makes the adapter's next submission behave a certain way. A venue cannot
	// be asked to time out on demand, so each harness arranges it however it can:
	// FakeBroker injects a fault, an HTTP adapter's test server changes what it
	// returns.
	//
	// A harness that cannot arrange a behaviour returns false, and the case is
	// skipped with that noted rather than silently passing.
	Arm func(t *testing.T, behaviour Behaviour, clientOrderID string) bool

	// Submissions reports how many requests reached the venue for a client order id.
	// Counting orders would miss a duplicate the venue deduplicated, and what the
	// contract cares about is what the platform sent.
	Submissions func(clientOrderID string) int
}

// Behaviour is a venue outcome a contract case needs.
type Behaviour string

const (
	BehaviourAccept           Behaviour = "ACCEPT"
	BehaviourReject           Behaviour = "REJECT"
	BehaviourTimeoutAfterRecv Behaviour = "TIMEOUT_AFTER_RECEIPT"
	BehaviourNotFound         Behaviour = "NOT_FOUND"
	BehaviourUnmappedStatus   Behaviour = "UNMAPPED_STATUS"
)

// Request is the canonical order every contract case submits.
func Request(clientOrderID string) broker.OrderRequest {
	return broker.OrderRequest{
		ClientOrderID: clientOrderID,
		TenantID:      "tenant_contract",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      f(10),
		LimitPrice:    f(50),
		TimeInForce:   intent.TIFDay,
	}
}

// Run executes the whole contract against one adapter.
func Run(t *testing.T, s Subject) {
	t.Helper()

	t.Run(s.Name+"/capabilities_are_honest", func(t *testing.T) { capabilitiesAreHonest(t, s) })
	t.Run(s.Name+"/paper_only", func(t *testing.T) { paperOnly(t, s) })
	t.Run(s.Name+"/client_order_id_required", func(t *testing.T) { clientOrderIDRequired(t, s) })
	t.Run(s.Name+"/accepted_order_echoes_our_id", func(t *testing.T) { echoesOurID(t, s) })
	t.Run(s.Name+"/lookup_by_our_id", func(t *testing.T) { lookupByOurID(t, s) })
	t.Run(s.Name+"/timeout_is_not_failure", func(t *testing.T) { timeoutIsNotFailure(t, s) })
	t.Run(s.Name+"/missing_order_is_not_found", func(t *testing.T) { missingOrderIsNotFound(t, s) })
	t.Run(s.Name+"/rejection_is_not_ambiguous", func(t *testing.T) { rejectionIsNotAmbiguous(t, s) })
	t.Run(s.Name+"/unmapped_status_errors", func(t *testing.T) { unmappedStatusErrors(t, s) })
	t.Run(s.Name+"/adapter_does_not_retry", func(t *testing.T) { adapterDoesNotRetry(t, s) })
	t.Run(s.Name+"/context_cancellation_respected", func(t *testing.T) { contextRespected(t, s) })
}

// An adapter's Capabilities must describe the adapter, not aspire.
func capabilitiesAreHonest(t *testing.T, s Subject) {
	caps := s.Adapter.Capabilities()

	if caps.Name == "" {
		t.Error("an adapter with no name cannot be named in evidence")
	}
	if len(caps.AssetClasses) == 0 {
		t.Error("an adapter that supports no asset class supports nothing")
	}
	if len(caps.OrderTypes) == 0 {
		t.Error("an adapter that supports no order type supports nothing")
	}

	// The one that is not optional. Reconciliation after an ambiguous timeout works
	// by looking an order up under our identifier; an adapter that cannot carry one
	// makes INV-004 unenforceable against its venue, and must not be wired in.
	if !caps.SupportsClientOrderID {
		t.Error("the adapter reports it cannot carry a client order id. That venue " +
			"cannot be reconciled after a timeout, so INV-004 cannot be enforced " +
			"against it, and it must not be connected")
	}
}

// V0 implements no real-money path, and an adapter that will not say so is one nobody
// should connect (spec section 59).
func paperOnly(t *testing.T, s Subject) {
	if !s.Adapter.Capabilities().PaperOnly {
		t.Error("the adapter does not declare itself paper-only; V0 has no real-money path")
	}

	account, err := s.Adapter.GetAccount(context.Background())
	if err != nil {
		t.Skipf("account unavailable in this harness: %v", err)
	}
	if !account.PaperTrading {
		t.Error("an account reached through this adapter reported itself as non-paper")
	}
}

// A submission with no client order id is refused, because it could never be
// reconciled.
func clientOrderIDRequired(t *testing.T, s Subject) {
	req := Request("")
	if _, err := s.Adapter.SubmitOrder(context.Background(), req); err == nil {
		t.Fatal("an order with no client order id was submitted; it could never be " +
			"looked up after a timeout (INV-004)")
	}
}

// The order the venue returns must carry our identifier, whether the venue echoed it
// or the adapter filled it back in. The core keys on it.
func echoesOurID(t *testing.T, s Subject) {
	const id = "coid-contract-echo"
	if !s.Arm(t, BehaviourAccept, id) {
		t.Skip("this harness cannot arrange an acceptance")
	}

	order, err := s.Adapter.SubmitOrder(context.Background(), Request(id))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.ClientOrderID != id {
		t.Errorf("client order id = %q, want %q; the core keys on its own identifier",
			order.ClientOrderID, id)
	}
	if order.State == "" {
		t.Error("the returned order has no state")
	}
}

// An order the venue holds must be findable by our identifier. This is the property
// reconciliation is built on, and the one most likely to differ between venues.
func lookupByOurID(t *testing.T, s Subject) {
	const id = "coid-contract-lookup"
	if !s.Arm(t, BehaviourAccept, id) {
		t.Skip("this harness cannot arrange an acceptance")
	}
	if _, err := s.Adapter.SubmitOrder(context.Background(), Request(id)); err != nil {
		t.Fatalf("submit: %v", err)
	}

	order, err := s.Adapter.Reconcile(context.Background(), id)
	if err != nil {
		t.Fatalf("an order the venue accepted could not be found by our identifier: %v", err)
	}
	if order.ClientOrderID != id {
		t.Errorf("reconcile returned %q, want %q", order.ClientOrderID, id)
	}
}

// A timeout means UNKNOWN, and the adapter must say so with ErrTimeout rather than a
// generic error. The caller's whole decision turns on telling those apart.
func timeoutIsNotFailure(t *testing.T, s Subject) {
	const id = "coid-contract-timeout"
	if !s.Arm(t, BehaviourTimeoutAfterRecv, id) {
		t.Skip("this harness cannot arrange a lost response")
	}

	_, err := s.Adapter.SubmitOrder(context.Background(), Request(id))
	if err == nil {
		t.Fatal("a lost response was reported as success")
	}
	if !errors.Is(err, broker.ErrTimeout) {
		t.Fatalf("error = %v; a lost response must be ErrTimeout so the caller "+
			"reconciles rather than assuming failure (INV-004)", err)
	}
}

// A venue with no such order returns ErrOrderNotFound, distinctly from a timeout.
// "Not found" and "could not ask" lead to different decisions.
func missingOrderIsNotFound(t *testing.T, s Subject) {
	const id = "coid-contract-missing"
	if !s.Arm(t, BehaviourNotFound, id) {
		t.Skip("this harness cannot arrange a missing order")
	}

	_, err := s.Adapter.Reconcile(context.Background(), id)
	if !errors.Is(err, broker.ErrOrderNotFound) {
		t.Fatalf("error = %v, want ErrOrderNotFound", err)
	}
	if errors.Is(err, broker.ErrTimeout) {
		t.Error("a missing order was reported as ambiguous; those lead to different decisions")
	}
}

// A refusal the venue actually gave is a fact, not an ambiguity. Reporting it as a
// timeout would send every rejection through reconciliation.
func rejectionIsNotAmbiguous(t *testing.T, s Subject) {
	const id = "coid-contract-reject"
	if !s.Arm(t, BehaviourReject, id) {
		t.Skip("this harness cannot arrange a rejection")
	}

	order, err := s.Adapter.SubmitOrder(context.Background(), Request(id))
	switch {
	case err != nil && errors.Is(err, broker.ErrTimeout):
		t.Error("a venue rejection was reported as ambiguous")
	case err != nil:
		// A returned error is fine: the venue said no.
	case order.State != broker.StateRejected:
		t.Errorf("state = %s; a refused order must be REJECTED", order.State)
	}
}

// A status the adapter cannot map must error rather than default. A gap in a mapping
// table that surfaces as a plausible wrong state is worse than one that surfaces as
// an error (INV-012).
func unmappedStatusErrors(t *testing.T, s Subject) {
	const id = "coid-contract-unmapped"
	if !s.Arm(t, BehaviourUnmappedStatus, id) {
		t.Skip("this harness cannot arrange an unmapped status")
	}

	order, err := s.Adapter.SubmitOrder(context.Background(), Request(id))
	if err == nil && order.State != "" && order.State != broker.StateUnknown {
		t.Errorf("an unmapped venue status became %s; a gap in the mapping must "+
			"surface as an error, not as a plausible state (INV-012)", order.State)
	}
}

// An adapter must not retry on its own. Deciding what to do about an ambiguous
// outcome is the caller's job, and an adapter that quietly retries takes it away.
func adapterDoesNotRetry(t *testing.T, s Subject) {
	const id = "coid-contract-noretry"
	if s.Submissions == nil {
		t.Skip("this harness cannot count submissions")
	}
	if !s.Arm(t, BehaviourTimeoutAfterRecv, id) {
		t.Skip("this harness cannot arrange a lost response")
	}

	before := s.Submissions(id)
	_, _ = s.Adapter.SubmitOrder(context.Background(), Request(id))

	if sent := s.Submissions(id) - before; sent != 1 {
		t.Errorf("one SubmitOrder call sent %d requests; an adapter that retries "+
			"removes the caller's ability to decide (spec section 18)", sent)
	}
}

// A cancelled context stops the adapter. An adapter that ignores it holds a request
// open past the deadline the caller set.
func contextRespected(t *testing.T, s Subject) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.Adapter.SubmitOrder(ctx, Request("coid-contract-cancelled"))
	if err == nil {
		t.Error("a submission proceeded under a cancelled context")
	}
}

// Deadline is a helper for harnesses that need one.
func Deadline() time.Duration { return 5 * time.Second }
