// Package alpaca adapts Alpaca's paper trading API to the broker.Adapter contract.
//
// ADR-012: this package knows Alpaca's shapes so the core never has to. Nothing here
// is exported into the domain, and the core does not import this package (there is a
// test that says so).
//
// The client is hand-written against Alpaca's documented REST API rather than using
// their SDK. An SDK would put a vendor's types one import away from the core, which
// is the exact pressure ADR-012 exists to resist, and this adapter needs six
// endpoints.
//
// # What has and has not been verified
//
// The request shaping and response parsing are covered by contract tests against an
// httptest server. This adapter has never been run against Alpaca's live paper
// sandbox: that needs credentials the project does not have, and no committed
// configuration references any. Treat "works end-to-end" as unproven until someone
// with a paper account runs it (spec section 56 item 20).
package alpaca

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

// Config holds connection settings.
//
// Credentials arrive from the caller, which gets them from a secret manager. They
// are never read from a file in this repository, never logged, and never returned
// through any method here (spec section 35).
type Config struct {
	BaseURL   string
	KeyID     string
	SecretKey string

	// SymbolFor maps a canonical instrument id to the venue's symbol. It is
	// injected because instrument reference data belongs to the platform, not to a
	// broker adapter, and because a wrong mapping here would send an order for the
	// wrong company.
	SymbolFor func(instrumentID string) (string, bool)

	HTTPClient *http.Client
	Timeout    time.Duration
}

// PaperBaseURL is Alpaca's paper trading endpoint. There is no live equivalent in
// this package on purpose: V0 implements no real-money path, and the way to keep it
// that way is to not write the URL down.
const PaperBaseURL = "https://paper-api.alpaca.markets"

type Adapter struct {
	cfg    Config
	client *http.Client
}

// ErrLiveTradingRefused is returned when configuration points at anything other than
// a paper endpoint.
var ErrLiveTradingRefused = errors.New("this adapter refuses any endpoint that is not paper trading")

func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = PaperBaseURL
	}
	if err := refuseLiveEndpoint(cfg.BaseURL); err != nil {
		return nil, err
	}
	if cfg.KeyID == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("alpaca credentials are required")
	}
	if cfg.SymbolFor == nil {
		return nil, fmt.Errorf("a symbol resolver is required; an adapter must not invent one")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: cfg.Timeout}
	}
	return &Adapter{cfg: cfg, client: client}, nil
}

// refuseLiveEndpoint is a guard, not a formality. The difference between paper and
// live at Alpaca is one hostname, and a configuration mistake would be the first
// real-money order this platform ever sent.
func refuseLiveEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable base URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())

	// Local test servers are permitted; they are how the contract tests run.
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	if !strings.HasPrefix(host, "paper-api.") {
		return fmt.Errorf("%w: %q", ErrLiveTradingRefused, raw)
	}
	return nil
}

func (a *Adapter) Capabilities() broker.Capabilities {
	return broker.Capabilities{
		Name:         "alpaca-paper",
		PaperOnly:    true,
		AssetClasses: []intent.AssetClass{intent.AssetEquity, intent.AssetETF},
		OrderTypes: []intent.OrderType{
			intent.OrderMarket, intent.OrderLimit, intent.OrderStop, intent.OrderStopLimit,
		},
		SupportsNotional:      true,
		SupportsExtendedHours: true,
		SupportsClientOrderID: true,
	}
}

// wire types. They exist only in this file and never cross the package boundary.

type wireOrder struct {
	ID             string `json:"id"`
	ClientOrderID  string `json:"client_order_id"`
	Status         string `json:"status"`
	FilledQty      string `json:"filled_qty"`
	FilledAvgPrice string `json:"filled_avg_price"`
	SubmittedAt    string `json:"submitted_at"`
	UpdatedAt      string `json:"updated_at"`
}

type wireError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func (a *Adapter) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("APCA-API-KEY-ID", a.cfg.KeyID)
	req.Header.Set("APCA-API-SECRET-KEY", a.cfg.SecretKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		// A transport failure is exactly the ambiguity INV-004 is about: the
		// request may or may not have reached the venue. It is reported as a
		// timeout so the caller reconciles rather than assuming.
		return fmt.Errorf("%w: %v", broker.ErrTimeout, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: reading response: %v", broker.ErrTimeout, err)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return broker.ErrOrderNotFound
	case resp.StatusCode >= 500:
		// The venue may have acted. Ambiguous, so: reconcile.
		return fmt.Errorf("%w: venue returned %d", broker.ErrTimeout, resp.StatusCode)
	case resp.StatusCode >= 400:
		var werr wireError
		_ = json.Unmarshal(raw, &werr)
		if werr.Message == "" {
			werr.Message = strings.TrimSpace(string(raw))
		}
		// A 4xx is the venue telling us no. That is a fact, not an ambiguity.
		return fmt.Errorf("venue rejected the request: %s", werr.Message)
	}

	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("venue returned unparseable JSON: %w", err)
		}
	}
	return nil
}

func (a *Adapter) SubmitOrder(ctx context.Context, req broker.OrderRequest) (broker.BrokerOrder, error) {
	symbol, err := venueSymbol(req, a.cfg.SymbolFor)
	if err != nil {
		return broker.BrokerOrder{}, err
	}
	if req.ClientOrderID == "" {
		return broker.BrokerOrder{}, fmt.Errorf("%w: client order id is required for reconciliation",
			broker.ErrUnsupported)
	}

	payload := map[string]any{
		"symbol":          symbol,
		"side":            strings.ToLower(string(req.Side)),
		"type":            alpacaOrderType(req.OrderType),
		"time_in_force":   strings.ToLower(string(req.TimeInForce)),
		"client_order_id": req.ClientOrderID,
		"extended_hours":  req.ExtendedHours,
	}
	switch {
	case req.Quantity != nil:
		// The exact decimal, as text. FormatFloat rendered whatever binary64 had
		// made of the value; String renders the value itself.
		payload["qty"] = req.Quantity.String()
	case req.Notional != nil:
		payload["notional"] = req.Notional.String()
	default:
		return broker.BrokerOrder{}, fmt.Errorf("%w: neither quantity nor notional", broker.ErrUnsupported)
	}
	if req.LimitPrice != nil {
		payload["limit_price"] = req.LimitPrice.String()
	}
	if req.StopPrice != nil {
		payload["stop_price"] = req.StopPrice.String()
	}

	var wire wireOrder
	if err := a.do(ctx, http.MethodPost, "/v2/orders", payload, &wire); err != nil {
		return broker.BrokerOrder{}, err
	}
	return toBrokerOrder(wire)
}

func (a *Adapter) GetOrder(ctx context.Context, clientOrderID string) (broker.BrokerOrder, error) {
	var wire wireOrder
	path := "/v2/orders:by_client_order_id?client_order_id=" + url.QueryEscape(clientOrderID)
	if err := a.do(ctx, http.MethodGet, path, nil, &wire); err != nil {
		return broker.BrokerOrder{}, err
	}
	return toBrokerOrder(wire)
}

// Reconcile is a lookup by our own identifier, which is the only handle that
// survives a lost response.
func (a *Adapter) Reconcile(ctx context.Context, clientOrderID string) (broker.BrokerOrder, error) {
	return a.GetOrder(ctx, clientOrderID)
}

func (a *Adapter) CancelOrder(ctx context.Context, clientOrderID string) error {
	order, err := a.GetOrder(ctx, clientOrderID)
	if err != nil {
		return err
	}
	if order.BrokerOrderID == "" {
		return fmt.Errorf("%w: no venue order id", broker.ErrOrderNotFound)
	}
	return a.do(ctx, http.MethodDelete, "/v2/orders/"+url.PathEscape(order.BrokerOrderID), nil, nil)
}

func (a *Adapter) GetOrders(ctx context.Context, since time.Time) ([]broker.BrokerOrder, error) {
	var wires []wireOrder
	path := "/v2/orders?status=all&after=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
	if err := a.do(ctx, http.MethodGet, path, nil, &wires); err != nil {
		return nil, err
	}
	out := make([]broker.BrokerOrder, 0, len(wires))
	for _, w := range wires {
		order, err := toBrokerOrder(w)
		if err != nil {
			// One unparseable order must not discard the rest, and must not be
			// guessed at either (INV-012).
			continue
		}
		out = append(out, order)
	}
	return out, nil
}

func (a *Adapter) GetPositions(ctx context.Context) ([]broker.Position, error) {
	var wires []struct {
		Symbol   string `json:"symbol"`
		Qty      string `json:"qty"`
		AvgPrice string `json:"avg_entry_price"`
	}
	if err := a.do(ctx, http.MethodGet, "/v2/positions", nil, &wires); err != nil {
		return nil, err
	}
	out := make([]broker.Position, 0, len(wires))
	for _, w := range wires {
		qty, err := venueNumber("qty", w.Qty)
		if err != nil {
			return nil, err
		}
		price, err := venueNumber("avg_entry_price", w.AvgPrice)
		if err != nil {
			return nil, err
		}
		// InstrumentID is left empty: mapping a venue symbol back to canonical
		// identity needs reference data this adapter does not have, and inventing
		// one would be exactly the ticker-as-identity mistake spec section 13
		// forbids.
		out = append(out, broker.Position{Symbol: w.Symbol, Quantity: qty, AveragePrice: price})
	}
	return out, nil
}

func (a *Adapter) GetAccount(ctx context.Context) (broker.Account, error) {
	var wire struct {
		ID          string `json:"id"`
		Currency    string `json:"currency"`
		Cash        string `json:"cash"`
		BuyingPower string `json:"buying_power"`
	}
	if err := a.do(ctx, http.MethodGet, "/v2/account", nil, &wire); err != nil {
		return broker.Account{}, err
	}
	cash, err := venueNumber("cash", wire.Cash)
	if err != nil {
		return broker.Account{}, err
	}
	bp, err := venueNumber("buying_power", wire.BuyingPower)
	if err != nil {
		return broker.Account{}, err
	}

	return broker.Account{
		AccountID:   wire.ID,
		Currency:    wire.Currency,
		Cash:        cash,
		BuyingPower: bp,
		// The constructor refuses any non-paper endpoint, so an account reached
		// through this adapter is a paper account by construction.
		PaperTrading: true,
	}, nil
}

func alpacaOrderType(o intent.OrderType) string {
	switch o {
	case intent.OrderMarket:
		return "market"
	case intent.OrderLimit:
		return "limit"
	case intent.OrderStop:
		return "stop"
	case intent.OrderStopLimit:
		return "stop_limit"
	}
	return string(o)
}

// venueNumber reads a numeric field the venue sent as text.
//
// Every one of these used to be `value, _ := strconv.ParseFloat(...)`, in a function whose
// own comment two lines down says it refuses what it cannot map rather than guessing. A
// filled_qty the adapter could not parse became a filled quantity of zero: an order that
// filled, recorded as having filled nothing, in the evidence chain and in the answer the
// caller was given. That is the canonical model corrupted by an adapter, which is INV-012
// exactly.
//
// An empty string is zero and nothing else is. Alpaca leaves filled_avg_price empty until
// something fills, which is the case that made the discarded error look harmless for as
// long as nobody looked.
func venueNumber(field, raw string) (float64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("venue sent %s=%q, which is not a number: %w", field, raw, err)
	}
	return value, nil
}

// toBrokerOrder converts a venue order into the canonical one, refusing anything it
// cannot map rather than guessing (INV-012).
func toBrokerOrder(w wireOrder) (broker.BrokerOrder, error) {
	state, ok := toExecutionState(w.Status)
	if !ok {
		return broker.BrokerOrder{}, fmt.Errorf("venue returned an unmapped status %q", w.Status)
	}

	filled, err := venueNumber("filled_qty", w.FilledQty)
	if err != nil {
		return broker.BrokerOrder{}, err
	}
	avg, err := venueNumber("filled_avg_price", w.FilledAvgPrice)
	if err != nil {
		return broker.BrokerOrder{}, err
	}

	order := broker.BrokerOrder{
		ClientOrderID:    w.ClientOrderID,
		BrokerOrderID:    w.ID,
		State:            state,
		FilledQuantity:   filled,
		AverageFillPrice: avg,
	}
	if t, err := time.Parse(time.RFC3339, w.SubmittedAt); err == nil {
		order.SubmittedAt = t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, w.UpdatedAt); err == nil {
		order.UpdatedAt = t.UTC()
	}
	return order, nil
}

// toExecutionState maps Alpaca statuses onto ours.
//
// Unmapped statuses return false rather than a default. A venue status the core has
// never heard of is not "probably accepted": it is a gap in this table, and the core
// finding out through a wrong state is worse than finding out through an error.
func toExecutionState(status string) (broker.ExecutionState, bool) {
	switch strings.ToLower(status) {
	case "new", "accepted", "pending_new", "accepted_for_bidding", "held", "replaced",
		"pending_replace", "pending_cancel", "done_for_day", "stopped", "suspended", "calculated":
		return broker.StateAccepted, true
	case "partially_filled":
		return broker.StatePartiallyFilled, true
	case "filled":
		return broker.StateFilled, true
	case "canceled", "cancelled":
		return broker.StateCancelled, true
	case "expired":
		return broker.StateExpired, true
	case "rejected":
		return broker.StateRejected, true
	}
	return "", false
}

// venueSymbol is the symbol this order goes to the venue with.
//
// The platform resolves it and puts it on the request: instrument reference data
// belongs to the platform (spec section 13), and OrderRequest.Symbol is where that
// resolution arrives. This used to ignore it and re-resolve through the injected
// mapping, which in the running gateway was a passthrough of the canonical id — so
// every real order carried "instr_us_equity_00206R102" where a ticker belonged, and
// Alpaca answered "asset not found".
//
// Nothing caught it because every test injected a real mapping, and the fake broker
// accepts any symbol. It took an order at a real venue to see it.
func venueSymbol(req broker.OrderRequest, fallback func(string) (string, bool)) (string, error) {
	if req.Symbol != "" {
		return req.Symbol, nil
	}
	if fallback != nil {
		if symbol, ok := fallback(req.InstrumentID); ok {
			return symbol, nil
		}
	}
	return "", fmt.Errorf("%w: no venue symbol for instrument %s; an adapter does not "+
		"guess one", broker.ErrUnsupported, req.InstrumentID)
}
