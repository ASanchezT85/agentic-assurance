// Package tradier adapts Tradier's sandbox brokerage API to broker.Adapter.
//
// It exists to test the abstraction, not because the platform needs two brokers
// (spec section 18, ADR-012). FakeBroker and the Alpaca adapter were written against
// the same contract at the same time by the same hand, which is a weaker proof than
// it looks: an abstraction shaped around its first two consumers will fit them.
//
// Tradier is chosen because its shapes differ from Alpaca's in ways that press on the
// contract rather than confirm it:
//
//   - Requests are form-encoded, not JSON.
//   - Authentication is a Bearer token, not a pair of custom headers.
//   - The status vocabulary is different and partly overlapping: "open" where Alpaca
//     says "new", "canceled" spelled the same but reached differently, plus an
//     "error" state Alpaca has no equivalent for.
//   - Orders are scoped to an account in the path, so the adapter needs an account
//     id that Alpaca infers from the key.
//   - The client order id is a "tag" with a restricted character set, which is the
//     one that actually found something. See ErrClientOrderIDUnsupported below.
//
// # What has and has not been verified
//
// Request shaping and response parsing are covered against an httptest server, and
// the adapter passes the same contract suite as FakeBroker. It has never been run
// against Tradier's sandbox: that needs credentials the project does not have.
package tradier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/intent"
)

// SandboxBaseURL is Tradier's sandbox. There is no production constant here, for the
// same reason the Alpaca adapter has none: V0 implements no real-money path, and the
// way to keep it that way is not to write the URL down.
const SandboxBaseURL = "https://sandbox.tradier.com"

// ErrLiveTradingRefused is returned for any endpoint that is not the sandbox.
var ErrLiveTradingRefused = errors.New("this adapter refuses any endpoint that is not the sandbox")

// ErrClientOrderIDUnsupported is returned when our identifier cannot be carried.
//
// This is the finding the second adapter produced. Tradier's order tag accepts only
// letters, digits and dashes, and our client order ids are derived from idempotency
// keys that may contain underscores. An adapter that silently rewrote the id would
// break reconciliation in the worst possible way: the order would exist at the venue
// under a name we could never look up, and INV-004 would be unenforceable against it
// while every test still passed.
//
// So the adapter refuses rather than transforms.
var ErrClientOrderIDUnsupported = errors.New("client order id cannot be carried by this venue")

// tagPattern is Tradier's constraint on the order tag.
var tagPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,255}$`)

type Config struct {
	BaseURL   string
	Token     string
	AccountID string

	// SymbolFor maps canonical instrument identity to the venue's symbol. Injected
	// for the same reason as in the Alpaca adapter: a ticker is not a canonical
	// identifier, and an adapter that guessed one would send an order for the wrong
	// company (spec section 13).
	SymbolFor func(instrumentID string) (string, bool)

	HTTPClient *http.Client
	Timeout    time.Duration
}

type Adapter struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Adapter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = SandboxBaseURL
	}
	if err := refuseLiveEndpoint(cfg.BaseURL); err != nil {
		return nil, err
	}
	switch {
	case cfg.Token == "":
		return nil, fmt.Errorf("a token is required")
	case cfg.AccountID == "":
		return nil, fmt.Errorf("an account id is required; this venue scopes orders by account")
	case cfg.SymbolFor == nil:
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

func refuseLiveEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable base URL: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	if host == "127.0.0.1" || host == "localhost" || host == "::1" {
		return nil
	}
	if !strings.HasPrefix(host, "sandbox.") {
		return fmt.Errorf("%w: %q", ErrLiveTradingRefused, raw)
	}
	return nil
}

func (a *Adapter) Capabilities() broker.Capabilities {
	return broker.Capabilities{
		Name:         "tradier-sandbox",
		PaperOnly:    true,
		AssetClasses: []intent.AssetClass{intent.AssetEquity, intent.AssetETF},
		OrderTypes: []intent.OrderType{
			intent.OrderMarket, intent.OrderLimit, intent.OrderStop, intent.OrderStopLimit,
		},
		// Tradier has no notional sizing: orders are in shares. The core already
		// knows what to do with this, because Capabilities exists so a venue can say
		// what it cannot express rather than failing at submission.
		SupportsNotional:      false,
		SupportsExtendedHours: true,
		SupportsClientOrderID: true,
	}
}

type wireOrder struct {
	ID                int64   `json:"id"`
	Status            string  `json:"status"`
	Tag               string  `json:"tag"`
	Quantity          float64 `json:"quantity"`
	ExecQuantity      float64 `json:"exec_quantity"`
	AvgFillPrice      float64 `json:"avg_fill_price"`
	CreateDate        string  `json:"create_date"`
	TransactionDate   string  `json:"transaction_date"`
	ReasonDescription string  `json:"reason_description"`
}

type wireOrderEnvelope struct {
	Order json.RawMessage `json:"order"`
}

type wireOrdersEnvelope struct {
	Orders struct {
		Order json.RawMessage `json:"order"`
	} `json:"orders"`
}

type wireFault struct {
	Fault struct {
		FaultString string `json:"faultstring"`
	} `json:"fault"`
	Errors struct {
		Error json.RawMessage `json:"error"`
	} `json:"errors"`
}

func (a *Adapter) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}

	req, err := http.NewRequestWithContext(ctx, method, a.cfg.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := a.client.Do(req)
	if err != nil {
		// The request may or may not have reached the venue. Reported as a timeout so
		// the caller reconciles rather than assumes (INV-004). The token is in a
		// header rather than the URL here, but the error is still not passed through,
		// because a transport error carries the URL and the habit is worth keeping.
		return fmt.Errorf("%w: tradier unreachable", broker.ErrTimeout)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: reading response", broker.ErrTimeout)
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return broker.ErrOrderNotFound
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: venue returned %d", broker.ErrTimeout, resp.StatusCode)
	case resp.StatusCode >= 400:
		return fmt.Errorf("venue rejected the request: %s", faultMessage(raw))
	}

	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("venue returned unparseable JSON: %w", err)
		}
	}
	return nil
}

func faultMessage(raw []byte) string {
	var fault wireFault
	if err := json.Unmarshal(raw, &fault); err == nil {
		if fault.Fault.FaultString != "" {
			return fault.Fault.FaultString
		}
		if len(fault.Errors.Error) > 0 {
			return strings.Trim(string(fault.Errors.Error), `"[]`)
		}
	}
	return strings.TrimSpace(string(raw))
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
	if !tagPattern.MatchString(req.ClientOrderID) {
		// Refused, never rewritten. An order at the venue under a name we cannot look
		// up is worse than an order that was not placed.
		return broker.BrokerOrder{}, fmt.Errorf(
			"%w: %q contains characters this venue's order tag does not accept "+
				"(letters, digits and dashes only). Rewriting it would leave an order "+
				"the platform could never reconcile",
			ErrClientOrderIDUnsupported, req.ClientOrderID)
	}
	if req.Quantity == nil {
		return broker.BrokerOrder{}, fmt.Errorf(
			"%w: this venue sizes orders in shares and cannot express a notional",
			broker.ErrUnsupported)
	}

	form := url.Values{}
	form.Set("class", "equity")
	form.Set("symbol", symbol)
	form.Set("side", tradierSide(req.Side))
	form.Set("quantity", strconv.FormatFloat(*req.Quantity, 'f', -1, 64))
	form.Set("type", tradierOrderType(req.OrderType))
	form.Set("duration", strings.ToLower(string(req.TimeInForce)))
	form.Set("tag", req.ClientOrderID)
	if req.LimitPrice != nil {
		form.Set("price", strconv.FormatFloat(*req.LimitPrice, 'f', -1, 64))
	}
	if req.StopPrice != nil {
		form.Set("stop", strconv.FormatFloat(*req.StopPrice, 'f', -1, 64))
	}

	var envelope wireOrderEnvelope
	path := "/v1/accounts/" + url.PathEscape(a.cfg.AccountID) + "/orders"
	if err := a.do(ctx, http.MethodPost, path, form, &envelope); err != nil {
		return broker.BrokerOrder{}, err
	}

	order, err := singleOrder(envelope.Order)
	if err != nil {
		return broker.BrokerOrder{}, err
	}
	// A submission response often omits the tag it was given. The core's identifier
	// is ours, so it is filled in from the request rather than trusted back.
	if order.ClientOrderID == "" {
		order.ClientOrderID = req.ClientOrderID
	}
	return order, nil
}

func (a *Adapter) GetOrder(ctx context.Context, clientOrderID string) (broker.BrokerOrder, error) {
	// Tradier has no lookup by tag, so the adapter lists and filters. That is a real
	// cost of this venue, and it belongs here rather than in the core: the contract
	// says "find this order by our identifier", and how is the adapter's problem.
	orders, err := a.GetOrders(ctx, time.Time{})
	if err != nil {
		return broker.BrokerOrder{}, err
	}
	for _, order := range orders {
		if order.ClientOrderID == clientOrderID {
			return order, nil
		}
	}
	return broker.BrokerOrder{}, broker.ErrOrderNotFound
}

func (a *Adapter) Reconcile(ctx context.Context, clientOrderID string) (broker.BrokerOrder, error) {
	return a.GetOrder(ctx, clientOrderID)
}

func (a *Adapter) GetOrders(ctx context.Context, since time.Time) ([]broker.BrokerOrder, error) {
	var envelope wireOrdersEnvelope
	path := "/v1/accounts/" + url.PathEscape(a.cfg.AccountID) + "/orders"
	if err := a.do(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return nil, err
	}
	return manyOrders(envelope.Orders.Order, since)
}

func (a *Adapter) CancelOrder(ctx context.Context, clientOrderID string) error {
	order, err := a.GetOrder(ctx, clientOrderID)
	if err != nil {
		return err
	}
	if order.BrokerOrderID == "" {
		return fmt.Errorf("%w: no venue order id", broker.ErrOrderNotFound)
	}
	path := "/v1/accounts/" + url.PathEscape(a.cfg.AccountID) + "/orders/" +
		url.PathEscape(order.BrokerOrderID)
	return a.do(ctx, http.MethodDelete, path, nil, nil)
}

func (a *Adapter) GetPositions(ctx context.Context) ([]broker.Position, error) {
	var envelope struct {
		Positions struct {
			Position json.RawMessage `json:"position"`
		} `json:"positions"`
	}
	path := "/v1/accounts/" + url.PathEscape(a.cfg.AccountID) + "/positions"
	if err := a.do(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return nil, err
	}

	type wirePosition struct {
		Symbol    string  `json:"symbol"`
		Quantity  float64 `json:"quantity"`
		CostBasis float64 `json:"cost_basis"`
	}
	var positions []wirePosition
	if err := unmarshalOneOrMany(envelope.Positions.Position, &positions); err != nil {
		return nil, nil
	}

	out := make([]broker.Position, 0, len(positions))
	for _, p := range positions {
		average := 0.0
		if p.Quantity != 0 {
			average = p.CostBasis / p.Quantity
		}
		// InstrumentID stays empty: mapping a venue symbol back to canonical identity
		// needs reference data this adapter does not have, and inventing one would be
		// the ticker-as-identity mistake spec section 13 forbids.
		out = append(out, broker.Position{Symbol: p.Symbol, Quantity: p.Quantity, AveragePrice: average})
	}
	return out, nil
}

func (a *Adapter) GetAccount(ctx context.Context) (broker.Account, error) {
	var envelope struct {
		Balances struct {
			AccountNumber string  `json:"account_number"`
			TotalCash     float64 `json:"total_cash"`
			BuyingPower   float64 `json:"stock_buying_power"`
		} `json:"balances"`
	}
	path := "/v1/accounts/" + url.PathEscape(a.cfg.AccountID) + "/balances"
	if err := a.do(ctx, http.MethodGet, path, nil, &envelope); err != nil {
		return broker.Account{}, err
	}
	return broker.Account{
		AccountID:   envelope.Balances.AccountNumber,
		Currency:    "USD",
		Cash:        envelope.Balances.TotalCash,
		BuyingPower: envelope.Balances.BuyingPower,
		// The constructor refuses any non-sandbox endpoint, so an account reached
		// through this adapter is a sandbox account by construction.
		PaperTrading: true,
	}, nil
}

func tradierSide(s intent.Side) string {
	if s == intent.SideSell {
		return "sell"
	}
	return "buy"
}

func tradierOrderType(o intent.OrderType) string {
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

// unmarshalOneOrMany handles a venue that returns an object for one result and an
// array for several. Alpaca does not do this; Tradier does, which is exactly the kind
// of venue-specific shape ADR-012 keeps out of the core.
func unmarshalOneOrMany(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `"null"` {
		return nil
	}
	if raw[0] == '[' {
		return json.Unmarshal(raw, out)
	}
	wrapped := append([]byte{'['}, raw...)
	wrapped = append(wrapped, ']')
	return json.Unmarshal(wrapped, out)
}

func singleOrder(raw json.RawMessage) (broker.BrokerOrder, error) {
	var wires []wireOrder
	if err := unmarshalOneOrMany(raw, &wires); err != nil {
		return broker.BrokerOrder{}, fmt.Errorf("venue order was not the expected shape: %w", err)
	}
	if len(wires) == 0 {
		return broker.BrokerOrder{}, fmt.Errorf("venue response contained no order")
	}
	return toBrokerOrder(wires[0])
}

func manyOrders(raw json.RawMessage, since time.Time) ([]broker.BrokerOrder, error) {
	var wires []wireOrder
	if err := unmarshalOneOrMany(raw, &wires); err != nil {
		return nil, fmt.Errorf("venue orders were not the expected shape: %w", err)
	}
	out := make([]broker.BrokerOrder, 0, len(wires))
	for _, w := range wires {
		order, err := toBrokerOrder(w)
		if err != nil {
			// One unmappable order must not discard the rest, and must not be
			// guessed at either (INV-012).
			continue
		}
		if !since.IsZero() && order.SubmittedAt.Before(since) {
			continue
		}
		out = append(out, order)
	}
	return out, nil
}

func toBrokerOrder(w wireOrder) (broker.BrokerOrder, error) {
	state, ok := toExecutionState(w.Status)
	if !ok {
		return broker.BrokerOrder{}, fmt.Errorf("venue returned an unmapped status %q", w.Status)
	}

	order := broker.BrokerOrder{
		ClientOrderID:    w.Tag,
		State:            state,
		FilledQuantity:   w.ExecQuantity,
		AverageFillPrice: w.AvgFillPrice,
		RejectReason:     w.ReasonDescription,
	}
	if w.ID != 0 {
		order.BrokerOrderID = strconv.FormatInt(w.ID, 10)
	}
	if t, err := time.Parse(time.RFC3339, w.CreateDate); err == nil {
		order.SubmittedAt = t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, w.TransactionDate); err == nil {
		order.UpdatedAt = t.UTC()
	}
	return order, nil
}

// toExecutionState maps Tradier's vocabulary onto ours.
//
// It overlaps Alpaca's only partly, which is the point of having a second adapter.
// "open" is Alpaca's "new"; "error" has no Alpaca equivalent and maps to REJECTED
// because the venue is telling us the order did not take. An unmapped status returns
// false rather than a plausible default: a gap in this table must surface as an error,
// not as a wrong state the core then acts on (INV-012).
func toExecutionState(status string) (broker.ExecutionState, bool) {
	switch strings.ToLower(status) {
	case "open", "pending", "calculated":
		return broker.StateAccepted, true
	case "partially_filled":
		return broker.StatePartiallyFilled, true
	case "filled":
		return broker.StateFilled, true
	case "canceled", "cancelled":
		return broker.StateCancelled, true
	case "expired":
		return broker.StateExpired, true
	case "rejected", "error":
		return broker.StateRejected, true
	}
	return "", false
}

// venueSymbol is the symbol this order goes to the venue with.
//
// The platform resolves it and puts it on the request (spec section 13); this used to
// ignore that and re-resolve through the injected mapping, which in the running
// gateway is a passthrough of the canonical instrument id.
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
