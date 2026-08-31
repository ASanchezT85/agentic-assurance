package alpaca

import (
	"agentic-assurance/internal/money"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

// These are contract tests: they prove request shaping and response parsing against
// a server that speaks Alpaca's documented shapes. They do not prove connectivity to
// Alpaca, and nothing here should be read as saying the adapter has been exercised
// against the real sandbox.

// f and q build the exact financial types a decoded envelope carries. Tests may start
// from a float literal for readability; the platform never does.
func f(v float64) *money.Amount {
	a, err := money.FromFloat(v)
	if err != nil {
		panic(err)
	}
	return &a
}

func q(v float64) *money.Quantity {
	x, err := money.QuantityFromFloat(v)
	if err != nil {
		panic(err)
	}
	return &x
}

func newTestAdapter(t *testing.T, handler http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	a, err := New(Config{
		BaseURL:   srv.URL,
		KeyID:     "test-key",
		SecretKey: "test-secret",
		SymbolFor: func(id string) (string, bool) {
			if id == "instr_us_equity_00206R102" {
				return "AAPL", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	return a, srv
}

func sampleRequest() broker.OrderRequest {
	return broker.OrderRequest{
		ClientOrderID: "coid_1",
		TenantID:      "tenant_acme",
		InstrumentID:  "instr_us_equity_00206R102",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      q(100),
		LimitPrice:    f(50.25),
		TimeInForce:   intent.TIFDay,
	}
}

// The guard that matters most in this package.
func TestLiveEndpointsAreRefused(t *testing.T) {
	live := []string{
		"https://api.alpaca.markets",
		"https://api.alpaca.markets/v2",
		"https://broker-api.alpaca.markets",
		"http://api.alpaca.markets",
	}
	for _, base := range live {
		_, err := New(Config{
			BaseURL: base, KeyID: "k", SecretKey: "s",
			SymbolFor: func(string) (string, bool) { return "AAPL", true },
		})
		if !errors.Is(err, ErrLiveTradingRefused) {
			t.Errorf("%s was accepted; V0 has no real-money path (spec section 59)", base)
		}
	}

	if _, err := New(Config{
		BaseURL: PaperBaseURL, KeyID: "k", SecretKey: "s",
		SymbolFor: func(string) (string, bool) { return "AAPL", true },
	}); err != nil {
		t.Errorf("the paper endpoint was refused: %v", err)
	}
}

func TestSubmitOrderShapesTheRequest(t *testing.T) {
	var got map[string]any
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/orders" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("APCA-API-KEY-ID") == "" || r.Header.Get("APCA-API-SECRET-KEY") == "" {
			t.Error("credentials were not sent")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(wireOrder{
			ID: "alp-1", ClientOrderID: "coid_1", Status: "new", FilledQty: "0",
		})
	})

	order, err := a.SubmitOrder(context.Background(), sampleRequest())
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	if got["symbol"] != "AAPL" {
		t.Errorf("symbol = %v", got["symbol"])
	}
	if got["client_order_id"] != "coid_1" {
		t.Errorf("client_order_id = %v; without it the order cannot be reconciled", got["client_order_id"])
	}
	if got["type"] != "limit" || got["side"] != "buy" || got["time_in_force"] != "day" {
		t.Errorf("order fields wrong: %v", got)
	}
	// The exact decimal, at the platform's scale. "50.2500" and "50.25" are the same
	// price to a venue; what matters is that the digits are the ones that were signed
	// rather than the shortest rendering of a float that approximated them.
	if got["qty"] != "100" || got["limit_price"] != "50.2500" {
		t.Errorf("sizing wrong: qty=%v limit=%v", got["qty"], got["limit_price"])
	}
	if order.State != broker.StateAccepted || order.BrokerOrderID != "alp-1" {
		t.Errorf("parsed order wrong: %+v", order)
	}
}

// An unknown instrument must not become a guessed symbol. Sending an order for the
// wrong company is the worst possible outcome of a mapping shortcut.
func TestUnknownInstrumentIsRefused(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was sent for an instrument with no symbol mapping")
	})

	req := sampleRequest()
	req.InstrumentID = "instr_something_unmapped"

	if _, err := a.SubmitOrder(context.Background(), req); !errors.Is(err, broker.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrUnsupported", err)
	}
}

// A 5xx may mean the venue acted. It must surface as a timeout so the caller
// reconciles rather than assuming failure (INV-004).
func TestServerErrorSurfacesAsAmbiguous(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	_, err := a.SubmitOrder(context.Background(), sampleRequest())
	if !errors.Is(err, broker.ErrTimeout) {
		t.Fatalf("error = %v; a 5xx is ambiguous and must reconcile, not fail", err)
	}
}

// A 4xx is the venue saying no. That is a fact, and must not be confused with
// ambiguity, or every rejection would trigger a reconciliation.
func TestClientErrorIsARejectionNotAnAmbiguity(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(wireError{Message: "insufficient buying power", Code: 40310000})
	})

	_, err := a.SubmitOrder(context.Background(), sampleRequest())
	if err == nil {
		t.Fatal("expected a rejection")
	}
	if errors.Is(err, broker.ErrTimeout) {
		t.Error("a 4xx was reported as ambiguous")
	}
	if !strings.Contains(err.Error(), "insufficient buying power") {
		t.Errorf("the venue's reason was lost: %v", err)
	}
}

func TestReconcileLooksUpByClientOrderID(t *testing.T) {
	var gotQuery string
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("client_order_id")
		_ = json.NewEncoder(w).Encode(wireOrder{
			ID: "alp-1", ClientOrderID: "coid_1", Status: "filled",
			FilledQty: "100", FilledAvgPrice: "50.10",
		})
	})

	order, err := a.Reconcile(context.Background(), "coid_1")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if gotQuery != "coid_1" {
		t.Errorf("looked up by %q, want the client order id", gotQuery)
	}
	if order.State != broker.StateFilled || order.FilledQuantity != 100 {
		t.Errorf("parsed order wrong: %+v", order)
	}
}

func TestMissingOrderIsNotFound(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	if _, err := a.Reconcile(context.Background(), "coid_missing"); !errors.Is(err, broker.ErrOrderNotFound) {
		t.Fatalf("error = %v, want ErrOrderNotFound", err)
	}
}

// Every status Alpaca documents must map, and anything else must fail loudly rather
// than defaulting to something plausible.
func TestStatusMapping(t *testing.T) {
	mapped := map[string]broker.ExecutionState{
		"new":              broker.StateAccepted,
		"accepted":         broker.StateAccepted,
		"pending_new":      broker.StateAccepted,
		"partially_filled": broker.StatePartiallyFilled,
		"filled":           broker.StateFilled,
		"canceled":         broker.StateCancelled,
		"expired":          broker.StateExpired,
		"rejected":         broker.StateRejected,
	}
	for status, want := range mapped {
		got, ok := toExecutionState(status)
		if !ok || got != want {
			t.Errorf("%q mapped to %q (ok=%v), want %q", status, got, ok, want)
		}
	}

	if _, ok := toExecutionState("some_new_status_alpaca_added"); ok {
		t.Error("an unmapped status was accepted; a gap in this table must surface as " +
			"an error, not as a plausible-looking state (INV-012)")
	}
}

func TestAccountIsAlwaysPaper(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"acc-1","currency":"USD","cash":"100000","buying_power":"200000"}`))
	})

	acct, err := a.GetAccount(context.Background())
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if !acct.PaperTrading {
		t.Error("an account reached through this adapter reported itself as non-paper")
	}
	if acct.Cash != 100000 || acct.BuyingPower != 200000 {
		t.Errorf("parsed account wrong: %+v", acct)
	}
}

func TestCredentialsAreNeverReturned(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {})

	caps := a.Capabilities()
	blob, _ := json.Marshal(caps)
	for _, secret := range []string{"test-key", "test-secret"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("credentials leaked through Capabilities (spec section 35)")
		}
	}
}

// The symbol the platform resolved is the symbol the venue gets.
//
// It used to be ignored in favour of the injected mapping, which in the running
// gateway was a passthrough of the canonical instrument id, so every real order asked
// Alpaca for an asset called "instr_us_equity_00206R102". Every test injected a real
// mapping and the fake broker accepts anything, so only an order at a real venue
// showed it.
func TestTheResolvedSymbolReachesTheVenue(t *testing.T) {
	var sent map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&sent)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"o-1","client_order_id":"coid-1","status":"accepted","symbol":"AAPL"}`))
	}))
	defer server.Close()

	adapter, err := New(Config{
		BaseURL: server.URL, KeyID: "k", SecretKey: "s",
		// What the gateway actually injects: a resolver that refuses, because the
		// platform has already done the resolving.
		SymbolFor: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	quantity := money.MustParseQuantity("1.0")
	if _, err := adapter.SubmitOrder(context.Background(), broker.OrderRequest{
		ClientOrderID: "coid-1",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		Side:          intent.SideBuy,
		OrderType:     intent.OrderMarket,
		Quantity:      &quantity,
		TimeInForce:   intent.TIFDay,
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	if sent["symbol"] != "AAPL" {
		t.Errorf("the venue was asked for %q, want AAPL", sent["symbol"])
	}
}

// And with neither a resolved symbol nor a mapping, the order is refused rather than
// sent under the canonical id.
func TestAnUnresolvedInstrumentIsRefusedRatherThanGuessed(t *testing.T) {
	adapter, err := New(Config{
		BaseURL: "https://paper-api.alpaca.markets", KeyID: "k", SecretKey: "s",
		SymbolFor: func(string) (string, bool) { return "", false },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	quantity := money.MustParseQuantity("1.0")
	_, err = adapter.SubmitOrder(context.Background(), broker.OrderRequest{
		ClientOrderID: "coid-1",
		InstrumentID:  "instr_us_equity_00206R102",
		Side:          intent.SideBuy,
		OrderType:     intent.OrderMarket,
		Quantity:      &quantity,
		TimeInForce:   intent.TIFDay,
	})
	if !errors.Is(err, broker.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// A number the venue sends as text is refused when it is not one.
//
// Found by the twelfth audit: three of the adapter's numeric fields were parsed with the
// error discarded, in a function whose comment says it refuses what it cannot map. The one
// that matters is filled_qty — an order that filled, recorded as having filled nothing,
// because a format the parser did not expect became a plausible zero (INV-012).
func TestAVenueNumberThatIsNotANumberIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		wants string
	}{
		{"a filled quantity with a thousands separator",
			`{"id":"alp-1","client_order_id":"coid-1","status":"filled","filled_qty":"1,000"}`,
			"filled_qty"},
		{"a filled price that is a word",
			`{"id":"alp-1","client_order_id":"coid-1","status":"filled","filled_qty":"10","filled_avg_price":"n/a"}`,
			"filled_avg_price"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			adapter, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(c.body))
			})
			_, err := adapter.Reconcile(context.Background(), "coid-1")
			if err == nil {
				t.Fatalf("the venue sent an unparseable %s and the adapter accepted it; "+
					"a number it cannot read must not become a zero the platform records "+
					"as fact (INV-012)", c.wants)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("the refusal does not name the field: %v", err)
			}
		})
	}
}

// And the empty string, which is what Alpaca sends before anything fills, is still zero.
func TestAnEmptyVenueNumberIsZero(t *testing.T) {
	adapter, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(
			`{"id":"alp-1","client_order_id":"coid-1","status":"new","filled_qty":"0","filled_avg_price":""}`))
	})

	order, err := adapter.Reconcile(context.Background(), "coid-1")
	if err != nil {
		t.Fatalf("an order that has not filled was refused: %v", err)
	}
	if order.AverageFillPrice != 0 || order.FilledQuantity != 0 {
		t.Errorf("an unfilled order came back as %v @ %v", order.FilledQuantity, order.AverageFillPrice)
	}
}

// The read side, which no test had ever executed: positions and the account.
func TestPositionsAndAccountAreParsedOrRefused(t *testing.T) {
	positions := `[{"symbol":"AAPL","qty":"10","avg_entry_price":"190.50"}]`
	account := `{"id":"acc-1","currency":"USD","cash":"1000.25","buying_power":"2000.50"}`

	adapter, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/positions":
			_, _ = w.Write([]byte(positions))
		case "/v2/account":
			_, _ = w.Write([]byte(account))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	held, err := adapter.GetPositions(context.Background())
	if err != nil {
		t.Fatalf("positions: %v", err)
	}
	if len(held) != 1 || held[0].Symbol != "AAPL" || held[0].Quantity != 10 || held[0].AveragePrice != 190.50 {
		t.Errorf("positions came back as %+v", held)
	}
	if held[0].InstrumentID != "" {
		t.Error("the adapter invented a canonical instrument id from a venue symbol (§13)")
	}

	acct, err := adapter.GetAccount(context.Background())
	if err != nil {
		t.Fatalf("account: %v", err)
	}
	if acct.Cash != 1000.25 || acct.BuyingPower != 2000.50 || acct.Currency != "USD" {
		t.Errorf("account came back as %+v", acct)
	}

	// And a position whose quantity is not a number is refused rather than counted as none.
	positions = `[{"symbol":"AAPL","qty":"ten","avg_entry_price":"190.50"}]`
	if _, err := adapter.GetPositions(context.Background()); err == nil {
		t.Error("a position with an unreadable quantity was reported as a position of zero")
	}
}
