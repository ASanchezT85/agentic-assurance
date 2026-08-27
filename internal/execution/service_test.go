package execution

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

var at = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

func envelope(key string) *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:  intent.SchemaVersion,
		EnvelopeID:     "env_" + key,
		IdempotencyKey: key,
		TenantID:       "tenant_acme",
		ReceivedAt:     at,
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         intent.SideBuy,
			OrderType:    intent.OrderLimit,
			Quantity:     f(100),
			LimitPrice:   f(50),
			TimeInForce:  intent.TIFDay,
		},
	}
}

func request(key string) broker.OrderRequest {
	return broker.OrderRequest{
		ClientOrderID: "coid_" + key,
		TenantID:      "tenant_acme",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      f(100),
		LimitPrice:    f(50),
		TimeInForce:   intent.TIFDay,
	}
}

func newService(t *testing.T) (*Service, *fakebroker.Broker, *MemoryStore, *MemoryCache) {
	t.Helper()
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return at })
	store := NewMemoryStore()
	cache := NewMemoryCache()
	return &Service{
		Broker: fake,
		Store:  store,
		Cache:  cache,
		Now:    func() time.Time { return at },
	}, fake, store, cache
}

// TestBrokerFailureMatrix is the Phase 5 exit criterion: every minimum case from
// spec section 54, each asserting the resulting state and, where it matters, that
// exactly one submission reached the venue.
func TestBrokerFailureMatrix(t *testing.T) {
	cases := []struct {
		name  string
		fault fakebroker.Fault

		wantState       broker.ExecutionState
		wantErr         error
		wantSubmissions int
	}{
		{
			name:            "request accepted",
			fault:           fakebroker.FaultNone,
			wantState:       broker.StateAccepted,
			wantSubmissions: 1,
		},
		{
			name:            "request rejected",
			fault:           fakebroker.FaultReject,
			wantState:       broker.StateRejected,
			wantSubmissions: 1,
		},
		{
			name: "timeout before the broker receives it",
			// Nothing exists at the venue, so reconciliation finds nothing. The
			// platform does not conclude "so I may submit again": not found and
			// not yet visible are indistinguishable from here.
			fault:           fakebroker.FaultTimeoutBeforeReceipt,
			wantState:       broker.StateUnknown,
			wantErr:         ErrUnresolved,
			wantSubmissions: 1,
		},
		{
			name:            "timeout after the broker receives it",
			fault:           fakebroker.FaultTimeoutAfterReceipt,
			wantState:       broker.StateAccepted,
			wantSubmissions: 1,
		},
		{
			name:            "partial fill",
			fault:           fakebroker.FaultPartialFill,
			wantState:       broker.StatePartiallyFilled,
			wantSubmissions: 1,
		},
		{
			name:            "fill arrives after the local timeout",
			fault:           fakebroker.FaultFillAfterLocalTimeout,
			wantState:       broker.StateFilled,
			wantSubmissions: 1,
		},
		{
			name:            "stale broker status",
			fault:           fakebroker.FaultStaleStatus,
			wantState:       broker.StateAccepted,
			wantSubmissions: 1,
		},
		{
			name:            "reconciliation cannot resolve",
			fault:           fakebroker.FaultUnreconcilable,
			wantState:       broker.StateUnknown,
			wantErr:         ErrUnresolved,
			wantSubmissions: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, fake, _, _ := newService(t)
			key := "k_" + tc.name
			fake.InjectFault("coid_"+key, tc.fault)

			got, err := svc.Submit(context.Background(), envelope(key), request(key))

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.State != tc.wantState {
				t.Errorf("state = %s, want %s", got.State, tc.wantState)
			}
			if n := fake.Submissions("coid_" + key); n != tc.wantSubmissions {
				t.Errorf("%d submissions reached the venue, want %d", n, tc.wantSubmissions)
			}
		})
	}
}

// The duplicate case: a network retry from the caller must not become a second
// order. This is the second half of the Phase 5 exit criterion.
func TestDuplicateNetworkRetryDoesNotSubmitTwice(t *testing.T) {
	svc, fake, _, _ := newService(t)

	first, err := svc.Submit(context.Background(), envelope("dup"), request("dup"))
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	second, err := svc.Submit(context.Background(), envelope("dup"), request("dup"))
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}

	if n := fake.Submissions("coid_dup"); n != 1 {
		t.Fatalf("%d submissions reached the venue; a duplicate request must not resubmit (INV-004)", n)
	}
	if !second.Replayed {
		t.Error("the second call did not report its outcome as replayed")
	}
	if second.State != first.State || second.BrokerOrderID != first.BrokerOrderID {
		t.Errorf("the duplicate returned a different outcome: %+v vs %+v", second, first)
	}
}

// A duplicate arriving after an ambiguous timeout is the dangerous one: the caller
// has no answer, so it retries, and the venue already has the order.
func TestDuplicateAfterAmbiguousTimeoutDoesNotSubmitTwice(t *testing.T) {
	svc, fake, _, _ := newService(t)
	fake.InjectFault("coid_amb", fakebroker.FaultTimeoutAfterReceipt)

	first, err := svc.Submit(context.Background(), envelope("amb"), request("amb"))
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	if first.State != broker.StateAccepted {
		t.Fatalf("reconciliation should have found the order, got %s", first.State)
	}

	second, err := svc.Submit(context.Background(), envelope("amb"), request("amb"))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}

	if n := fake.Submissions("coid_amb"); n != 1 {
		t.Fatalf("%d submissions reached the venue after an ambiguous timeout (INV-004)", n)
	}
	if !second.Replayed {
		t.Error("the retry was not served from the record")
	}
}

// A record left PENDING by a crashed attempt must reconcile, not resubmit.
func TestPendingRecordReconcilesRatherThanResubmits(t *testing.T) {
	svc, fake, store, _ := newService(t)

	// Simulate a process that claimed the key and died before resolving. The
	// order does exist at the venue.
	if _, _, err := store.Claim(context.Background(), Record{
		TenantID:       "tenant_acme",
		IdempotencyKey: "crash",
		ClientOrderID:  "coid_crash",
		State:          RecordPending,
		CreatedAt:      at,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := fake.SubmitOrder(context.Background(), request("crash")); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	before := fake.Submissions("coid_crash")

	got, err := svc.Submit(context.Background(), envelope("crash"), request("crash"))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if n := fake.Submissions("coid_crash"); n != before {
		t.Fatalf("a pending record caused a resubmission (INV-004)")
	}
	if got.State != broker.StateAccepted {
		t.Errorf("state = %s, want ACCEPTED from reconciliation", got.State)
	}
}

// A pending record whose order does not exist at the venue stays unresolved and goes
// to an operator. It does not become a new submission.
func TestPendingRecordWithNoOrderStaysUnresolved(t *testing.T) {
	svc, fake, store, _ := newService(t)

	if _, _, err := store.Claim(context.Background(), Record{
		TenantID:       "tenant_acme",
		IdempotencyKey: "orphan",
		ClientOrderID:  "coid_orphan",
		State:          RecordPending,
		CreatedAt:      at,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	got, err := svc.Submit(context.Background(), envelope("orphan"), request("orphan"))
	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("error = %v, want ErrUnresolved", err)
	}
	if got.State != broker.StateUnknown {
		t.Errorf("state = %s, want UNKNOWN", got.State)
	}
	if n := fake.Submissions("coid_orphan"); n != 0 {
		t.Fatalf("%d submissions; an unresolvable pending record must not resubmit (INV-004)", n)
	}
}

// The cancellation race of section 54: a cancel that arrives after the fill.
func TestCancelRace(t *testing.T) {
	svc, fake, _, _ := newService(t)

	if _, err := svc.Submit(context.Background(), envelope("race"), request("race")); err != nil {
		t.Fatalf("submit: %v", err)
	}
	fake.InjectFault("coid_race", fakebroker.FaultCancelRace)

	if err := fake.CancelOrder(context.Background(), "coid_race"); err == nil {
		t.Fatal("cancelling an already-filled order reported success")
	}

	order, err := fake.GetOrder(context.Background(), "coid_race")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if order.State != broker.StateFilled {
		t.Errorf("state = %s; a lost cancel race leaves the order filled", order.State)
	}
}

// The store is authoritative control state, so an unreadable one denies rather than
// letting the submission proceed unrecorded.
func TestUnreadableStoreDoesNotSubmit(t *testing.T) {
	svc, fake, store, _ := newService(t)
	store.FailWith = errors.New("connection refused")

	if _, err := svc.Submit(context.Background(), envelope("down"), request("down")); err == nil {
		t.Fatal("a submission proceeded with the idempotency store unavailable")
	}
	if n := fake.Submissions("coid_down"); n != 0 {
		t.Fatalf("%d submissions reached the venue with no idempotency record (INV-004)", n)
	}
}

// There is no code path that submits twice. The absence is the guarantee, so it is
// asserted structurally as well as behaviourally.
func TestServiceExposesNoResubmitPath(t *testing.T) {
	svc, fake, _, _ := newService(t)

	for i := 0; i < 5; i++ {
		if _, err := svc.Submit(context.Background(), envelope("many"), request("many")); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if n := fake.Submissions("coid_many"); n != 1 {
		t.Fatalf("%d submissions after five identical calls (INV-004)", n)
	}
}
