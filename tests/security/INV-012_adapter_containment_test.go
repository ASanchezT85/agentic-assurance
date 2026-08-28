package security

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
)

// INV-012: a broker adapter failure cannot corrupt the canonical core domain model.
//
// Adapters talk to systems nobody here controls. They will return fields the core
// does not expect, states it has never heard of, quantities that make no sense, and
// occasionally an order belonging to somebody else. None of that may become the
// platform's record of what happened.

// misbehaving is an adapter that returns garbage on purpose.
type misbehaving struct {
	order broker.BrokerOrder
	err   error
	calls int
}

func (m *misbehaving) Capabilities() broker.Capabilities {
	return broker.Capabilities{Name: "misbehaving", PaperOnly: true, SupportsClientOrderID: true}
}

func (m *misbehaving) SubmitOrder(context.Context, broker.OrderRequest) (broker.BrokerOrder, error) {
	m.calls++
	return m.order, m.err
}

func (m *misbehaving) Reconcile(context.Context, string) (broker.BrokerOrder, error) {
	return broker.BrokerOrder{}, broker.ErrOrderNotFound
}

func (m *misbehaving) GetOrder(context.Context, string) (broker.BrokerOrder, error) {
	return broker.BrokerOrder{}, broker.ErrOrderNotFound
}
func (m *misbehaving) CancelOrder(context.Context, string) error { return nil }
func (m *misbehaving) GetOrders(context.Context, time.Time) ([]broker.BrokerOrder, error) {
	return nil, nil
}
func (m *misbehaving) GetPositions(context.Context) ([]broker.Position, error) { return nil, nil }
func (m *misbehaving) GetAccount(context.Context) (broker.Account, error) {
	return broker.Account{PaperTrading: true}, nil
}

func serviceWith(adapter broker.Adapter) (*execution.Service, *execution.MemoryStore) {
	store := execution.NewMemoryStore()
	return &execution.Service{
		Broker: adapter,
		Store:  store,
		Now:    func() time.Time { return execAt },
	}, store
}

// Garbage from an adapter must never be written into the record as if it were fact.
func TestAdapterGarbageDoesNotReachTheRecord(t *testing.T) {
	cases := []struct {
		name  string
		order broker.BrokerOrder
	}{
		{
			name: "somebody else's order",
			order: broker.BrokerOrder{
				ClientOrderID: "coid_a_different_order",
				State:         broker.StateFilled,
			},
		},
		{
			name:  "no state at all",
			order: broker.BrokerOrder{ClientOrderID: "coid_garbage"},
		},
		{
			name: "a state the core has never heard of",
			order: broker.BrokerOrder{
				ClientOrderID: "coid_garbage",
				State:         broker.ExecutionState("QUANTUM_SUPERPOSITION"),
			},
		},
		{
			name: "negative filled quantity",
			order: broker.BrokerOrder{
				ClientOrderID:  "coid_garbage",
				State:          broker.StatePartiallyFilled,
				FilledQuantity: -50,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := serviceWith(&misbehaving{order: tc.order})

			got, err := svc.Submit(context.Background(), execEnvelope("garbage"), execRequest("garbage"))

			// The submission cannot be treated as successful, and the record must
			// not carry the adapter's nonsense.
			if err == nil && got.State == tc.order.State && tc.order.State != "" {
				t.Fatalf("the core accepted %s from a misbehaving adapter (INV-012)", tc.order.State)
			}
			if got.State != broker.StateUnknown && got.State != "" {
				t.Errorf("state = %s; unusable adapter output leaves the outcome UNKNOWN", got.State)
			}

			rec, loadErr := store.Load(context.Background(), "tenant_acme", "garbage")
			if loadErr != nil {
				return // no record is also acceptable containment
			}
			if rec.State == execution.RecordResolved {
				t.Errorf("a record was resolved from unusable adapter output: %+v (INV-012)", rec.Outcome)
			}
			if rec.Outcome.ClientOrderID != "" && rec.Outcome.ClientOrderID != "coid_garbage" {
				t.Errorf("another order's identifier entered the record: %q (INV-012)",
					rec.Outcome.ClientOrderID)
			}
		})
	}
}

// An adapter that simply errors produces a recorded rejection, not a corrupted one.
func TestAdapterErrorIsRecordedAsARejectionNotAsGarbage(t *testing.T) {
	svc, store := serviceWith(&misbehaving{err: errors.New("venue said no")})

	got, err := svc.Submit(context.Background(), execEnvelope("refused"), execRequest("refused"))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.State != broker.StateRejected {
		t.Errorf("state = %s, want REJECTED", got.State)
	}
	if got.ClientOrderID != "coid_refused" {
		t.Errorf("client order id = %q; the core keeps its own identifier", got.ClientOrderID)
	}

	rec, err := store.Load(context.Background(), "tenant_acme", "refused")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.Outcome.State != broker.StateRejected {
		t.Errorf("record state = %s", rec.Outcome.State)
	}
}

// The core domain must not import any adapter. ADR-012 says the core depends on the
// abstraction; this is the check that says it still does.
func TestCoreDoesNotImportAnyAdapter(t *testing.T) {
	for _, dir := range []string{
		"../../internal/broker", "../../internal/execution", "../../internal/intent",
		"../../internal/authority", "../../internal/identity", "../../internal/policy",
		"../../internal/fleet", "../../internal/evidence", "../../internal/incident",
	} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				for _, imp := range file.Imports {
					name := strings.Trim(imp.Path.Value, `"`)
					if strings.Contains(name, "agentic-assurance/adapters/") {
						t.Errorf("%s imports %s; the core depends on the abstraction, "+
							"never on an adapter (ADR-012, INV-012)", path, name)
					}
					if strings.Contains(strings.ToLower(name), "alpaca") {
						t.Errorf("%s imports %s; the core must not know any venue's types "+
							"(ADR-012)", path, name)
					}
				}
			}
		}
	}
}

// The canonical order type must carry no venue-specific field. One leaks in as a
// convenience and then everything downstream depends on that venue.
func TestCanonicalOrderHasNoVenueSpecificFields(t *testing.T) {
	raw, err := os.ReadFile("../../internal/broker/adapter.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := strings.ToLower(string(raw))

	for _, venue := range []string{"alpaca", "interactivebrokers", "tradier", "schwab"} {
		if strings.Contains(body, venue) {
			t.Errorf("internal/broker/adapter.go mentions %q; venue specifics belong in "+
				"that venue's adapter (ADR-012)", venue)
		}
	}
}
