package contract

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"agentic-assurance/adapters/alpaca"
	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/adapters/tradier"
	"agentic-assurance/internal/broker"
)

// Every adapter runs the same contract. That is the point: three implementations, one
// definition of what the core depends on, and a new venue finds out whether the
// abstraction fits before any code depends on it.

func TestFakeBrokerSatisfiesTheContract(t *testing.T) {
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC) })

	Run(t, Subject{
		Name:    "fakebroker",
		Adapter: fake,
		Arm: func(_ *testing.T, behaviour Behaviour, clientOrderID string) bool {
			switch behaviour {
			case BehaviourAccept:
				fake.InjectFault(clientOrderID, fakebroker.FaultNone)
			case BehaviourReject:
				fake.InjectFault(clientOrderID, fakebroker.FaultReject)
			case BehaviourTimeoutAfterRecv:
				fake.InjectFault(clientOrderID, fakebroker.FaultTimeoutAfterReceipt)
			case BehaviourNotFound:
				// An order that was never submitted is simply absent.
			case BehaviourUnmappedStatus:
				// FakeBroker only produces states the core knows. It cannot arrange
				// this, and saying so is better than a case that passes vacuously.
				return false
			}
			return true
		},
		Submissions: fake.Submissions,
	})
}

// httpVenue is a programmable server standing in for a REST broker, so the two HTTP
// adapters can be driven through the same behaviours.
type httpVenue struct {
	mu        sync.Mutex
	behaviour map[string]Behaviour
	requests  map[string]int
	orders    map[string]bool

	// respond writes the venue-specific body for a behaviour.
	respond func(w http.ResponseWriter, r *http.Request, v *httpVenue)
}

func newHTTPVenue(respond func(http.ResponseWriter, *http.Request, *httpVenue)) *httpVenue {
	return &httpVenue{
		behaviour: map[string]Behaviour{},
		requests:  map[string]int{},
		orders:    map[string]bool{},
		respond:   respond,
	}
}

func (v *httpVenue) arm(clientOrderID string, b Behaviour) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.behaviour[clientOrderID] = b
}

func (v *httpVenue) count(clientOrderID string) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.requests[clientOrderID]
}

func (v *httpVenue) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.respond(w, r, v)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// behaviourFor finds the armed behaviour for whichever client order id appears in the
// request, and records the submission.
func (v *httpVenue) behaviourFor(r *http.Request, id string, isSubmit bool) Behaviour {
	v.mu.Lock()
	defer v.mu.Unlock()
	if isSubmit && id != "" {
		v.requests[id]++
		if v.behaviour[id] != BehaviourTimeoutAfterRecv {
			v.orders[id] = true
		} else {
			v.orders[id] = true // the venue took it; the response is what was lost
		}
	}
	return v.behaviour[id]
}

func TestAlpacaSatisfiesTheContract(t *testing.T) {
	venue := newHTTPVenue(func(w http.ResponseWriter, r *http.Request, v *httpVenue) {
		// The account endpoint, so the paper-only case is checked rather than
		// skipped. It is a safety property, and a skipped safety check is one nobody
		// is relying on and everybody assumes.
		if r.URL.Path == "/v2/account" {
			_, _ = w.Write([]byte(`{"id":"acc-1","currency":"USD","cash":"100000","buying_power":"200000"}`))
			return
		}

		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			id, _ := body["client_order_id"].(string)

			switch v.behaviourFor(r, id, true) {
			case BehaviourReject:
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"message":"insufficient buying power","code":40310000}`))
			case BehaviourTimeoutAfterRecv:
				w.WriteHeader(http.StatusBadGateway)
			case BehaviourUnmappedStatus:
				_, _ = w.Write([]byte(`{"id":"alp-1","client_order_id":"` + id +
					`","status":"a_status_alpaca_added_last_week","filled_qty":"0"}`))
			default:
				_, _ = w.Write([]byte(`{"id":"alp-1","client_order_id":"` + id +
					`","status":"new","filled_qty":"0"}`))
			}
			return
		}

		id := r.URL.Query().Get("client_order_id")
		v.mu.Lock()
		exists := v.orders[id]
		v.mu.Unlock()
		if !exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"id":"alp-1","client_order_id":"` + id +
			`","status":"new","filled_qty":"0"}`))
	})

	srv := venue.server(t)
	adapter, err := alpaca.New(alpaca.Config{
		BaseURL:   srv.URL,
		KeyID:     "test-key",
		SecretKey: "test-secret",
		SymbolFor: func(string) (string, bool) { return "AAPL", true },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	Run(t, Subject{
		Name:        "alpaca-paper",
		Adapter:     adapter,
		Arm:         func(_ *testing.T, b Behaviour, id string) bool { venue.arm(id, b); return true },
		Submissions: venue.count,
	})
}

func TestTradierSatisfiesTheContract(t *testing.T) {
	venue := newHTTPVenue(func(w http.ResponseWriter, r *http.Request, v *httpVenue) {
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			id := r.Form.Get("tag")

			switch v.behaviourFor(r, id, true) {
			case BehaviourReject:
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"errors":{"error":["insufficient buying power"]}}`))
			case BehaviourTimeoutAfterRecv:
				w.WriteHeader(http.StatusServiceUnavailable)
			case BehaviourUnmappedStatus:
				_, _ = w.Write([]byte(`{"order":{"id":42,"status":"held_for_review","tag":"` +
					id + `","quantity":10}}`))
			default:
				_, _ = w.Write([]byte(`{"order":{"id":42,"status":"open","tag":"` +
					id + `","quantity":10}}`))
			}
			return
		}

		// Tradier has no lookup by tag, so the adapter lists. Return every order the
		// venue holds, in the single-object shape it uses for one result.
		v.mu.Lock()
		ids := make([]string, 0, len(v.orders))
		for id := range v.orders {
			ids = append(ids, id)
		}
		v.mu.Unlock()

		if len(ids) == 0 {
			_, _ = w.Write([]byte(`{"orders":"null"}`))
			return
		}
		if len(ids) == 1 {
			_, _ = w.Write([]byte(`{"orders":{"order":{"id":42,"status":"open","tag":"` +
				ids[0] + `","quantity":10}}}`))
			return
		}
		body := `{"orders":{"order":[`
		for i, id := range ids {
			if i > 0 {
				body += ","
			}
			body += `{"id":42,"status":"open","tag":"` + id + `","quantity":10}`
		}
		_, _ = w.Write([]byte(body + `]}}`))
	})

	srv := venue.server(t)
	adapter, err := tradier.New(tradier.Config{
		BaseURL:   srv.URL,
		Token:     "test-token",
		AccountID: "VA000001",
		SymbolFor: func(string) (string, bool) { return "AAPL", true },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	Run(t, Subject{
		Name:        "tradier-sandbox",
		Adapter:     adapter,
		Arm:         func(_ *testing.T, b Behaviour, id string) bool { venue.arm(id, b); return true },
		Submissions: venue.count,
	})
}

// The finding the second adapter produced.
//
// Tradier's order tag accepts letters, digits and dashes. Client order ids derived
// from idempotency keys may contain underscores, and the platform generates them that
// way. The adapter refuses rather than rewriting, because an order at the venue under
// a name we could never look up is worse than an order that was not placed: it exists,
// it may fill, and reconciliation would report NOT FOUND forever.
func TestTradierRefusesAnIdentifierItCannotCarry(t *testing.T) {
	srv := newHTTPVenue(func(w http.ResponseWriter, r *http.Request, v *httpVenue) {
		t.Error("a request was sent for an identifier the venue cannot carry")
	}).server(t)

	adapter, err := tradier.New(tradier.Config{
		BaseURL:   srv.URL,
		Token:     "test-token",
		AccountID: "VA000001",
		SymbolFor: func(string) (string, bool) { return "AAPL", true },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// The shape the platform actually produces: coid_ plus the idempotency key.
	_, err = adapter.SubmitOrder(t.Context(), Request("coid_idem_01J8Z3K9QW"))
	if err == nil {
		t.Fatal("an identifier the venue cannot carry was submitted anyway")
	}
	if !isUnsupportedIdentifier(err) {
		t.Errorf("error = %v; the refusal must name the reason, because the fix is in "+
			"how the platform generates identifiers", err)
	}
}

func isUnsupportedIdentifier(err error) bool {
	for err != nil {
		if err == tradier.ErrClientOrderIDUnsupported {
			return true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

var _ = broker.ErrTimeout
