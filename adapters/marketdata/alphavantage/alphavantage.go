// Package alphavantage supplies venue volume for the participation component of the
// Fleet Risk Vector.
//
// ADR-019 makes market data an optional adapter and is explicit about what happens
// without one: P is UNKNOWN and is never estimated from our own observed flow. This
// package narrows that gap, and it is equally explicit about the gap it does not
// close.
//
// # What the free tier can and cannot answer
//
// Alpha Vantage's free plan serves GLOBAL_QUOTE, which returns the latest trading
// day's total volume and last price. TIME_SERIES_INTRADAY is a premium endpoint, and
// the free key is capped at 25 requests per day.
//
// So this adapter can answer "what did this instrument trade over a whole session"
// and nothing finer. The fleet engine measures cohorts over windows of seconds and
// minutes, and prorating a daily volume across those windows would assume trading is
// spread evenly through the day. It is not: volume is heavily weighted to the open
// and the close, so a prorated denominator would understate participation at midday
// and overstate it at the open, in both cases producing a number that looks precise
// and is not.
//
// The adapter therefore returns "unknown" for any window shorter than a session,
// rather than a figure nobody should act on (P-004). Making P work for short windows
// needs an intraday feed, which needs a paid plan or a different provider.
package alphavantage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentic-assurance/internal/fleet"
)

// Config holds connection settings.
//
// The API key arrives from the caller, which reads it from the environment or a
// secret manager. It is never read from a file in this repository and never appears
// in an error message or a log line (spec section 35).
type Config struct {
	APIKey  string
	BaseURL string

	// SymbolFor maps a canonical instrument id to a venue symbol. Injected for the
	// same reason as in the Alpaca adapter: instrument reference data belongs to
	// the platform, and a ticker is not a canonical identifier (spec section 13).
	SymbolFor func(instrumentID string) (string, bool)

	// DailyRequestBudget is the free plan's cap. Exceeding it does not fail loudly
	// at the provider; it returns an explanatory JSON body that a careless parser
	// would read as "no data". The budget is enforced here so the adapter reports a
	// rate limit rather than silently reporting no volume.
	DailyRequestBudget int

	// CacheTTL is how long a quote is reused. The underlying figure is a daily
	// total, so caching for hours loses nothing and is the only way 25 requests a
	// day covers a fleet.
	CacheTTL time.Duration

	HTTPClient *http.Client
	Now        func() time.Time
}

const defaultBaseURL = "https://www.alphavantage.co"

// ErrRateLimited is returned when the daily budget is spent.
var ErrRateLimited = errors.New("alpha vantage daily request budget is spent")

// ErrPremiumEndpoint is returned when the provider refuses a free key.
var ErrPremiumEndpoint = errors.New("alpha vantage endpoint requires a paid plan")

// ErrWindowTooShort is returned when the requested window is finer than the data.
var ErrWindowTooShort = errors.New("alpha vantage free tier reports daily totals only")

// MinimumWindow is the shortest window this adapter will answer for.
//
// Six hours is roughly a US trading session. Anything shorter would need the daily
// total prorated, and intraday volume is not uniform.
const MinimumWindow = 6 * time.Hour

type quote struct {
	symbol     string
	price      float64
	volume     float64
	tradingDay string
	fetchedAt  time.Time
}

// Adapter implements fleet.MarketData.
type Adapter struct {
	cfg Config

	mu        sync.Mutex
	cache     map[string]quote
	spent     int
	budgetDay string
}

func New(cfg Config) (*Adapter, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("an API key is required")
	}
	if cfg.SymbolFor == nil {
		return nil, fmt.Errorf("a symbol resolver is required; an adapter must not invent one")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.DailyRequestBudget <= 0 {
		cfg.DailyRequestBudget = 25
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 4 * time.Hour
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Adapter{cfg: cfg, cache: map[string]quote{}}, nil
}

// VenueVolume returns traded notional for an instrument in a window.
//
// It returns false rather than a guess whenever it cannot answer honestly: an
// unmapped instrument, a window shorter than a session, a spent budget, or a
// provider response it does not understand. Every one of those becomes P=UNKNOWN
// upstream, which is the correct outcome (ADR-019).
func (a *Adapter) VenueVolume(instrumentID string, w fleet.Window) (float64, bool) {
	notional, _, err := a.VenueVolumeE(context.Background(), instrumentID, w)
	return notional, err == nil
}

// VenueVolumeE is VenueVolume with the reason attached.
//
// The interface fleet consumes returns only a bool, because a risk component does
// not carry provider errors. Operations needs the reason, so it is available here
// and the two do not have to disagree.
func (a *Adapter) VenueVolumeE(ctx context.Context, instrumentID string, w fleet.Window) (float64, string, error) {
	if w.Duration() < MinimumWindow {
		return 0, "", fmt.Errorf("%w: window of %s is shorter than a session, and prorating "+
			"a daily total across it would assume uniform intraday volume",
			ErrWindowTooShort, w.Duration())
	}

	symbol, ok := a.cfg.SymbolFor(instrumentID)
	if !ok {
		return 0, "", fmt.Errorf("no symbol mapping for instrument %s", instrumentID)
	}

	q, err := a.quoteFor(ctx, symbol)
	if err != nil {
		return 0, "", err
	}

	// Notional, not share count. The risk vector divides cohort gross notional by
	// this, so returning shares would produce a ratio of unlike quantities.
	return q.volume * q.price, q.tradingDay, nil
}

func (a *Adapter) quoteFor(ctx context.Context, symbol string) (quote, error) {
	now := a.cfg.Now()

	a.mu.Lock()
	if cached, ok := a.cache[symbol]; ok && now.Sub(cached.fetchedAt) < a.cfg.CacheTTL {
		a.mu.Unlock()
		return cached, nil
	}

	// The budget resets daily, like the provider's.
	day := now.UTC().Format("2006-01-02")
	if a.budgetDay != day {
		a.budgetDay, a.spent = day, 0
	}
	if a.spent >= a.cfg.DailyRequestBudget {
		a.mu.Unlock()
		return quote{}, fmt.Errorf("%w: %d of %d used today", ErrRateLimited, a.spent, a.cfg.DailyRequestBudget)
	}
	a.spent++
	a.mu.Unlock()

	q, err := a.fetch(ctx, symbol)
	if err != nil {
		return quote{}, err
	}
	q.fetchedAt = now

	a.mu.Lock()
	a.cache[symbol] = q
	a.mu.Unlock()
	return q, nil
}

func (a *Adapter) fetch(ctx context.Context, symbol string) (quote, error) {
	params := url.Values{}
	params.Set("function", "GLOBAL_QUOTE")
	params.Set("symbol", symbol)
	params.Set("apikey", a.cfg.APIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		a.cfg.BaseURL+"/query?"+params.Encode(), nil)
	if err != nil {
		return quote{}, err
	}

	resp, err := a.cfg.HTTPClient.Do(req)
	if err != nil {
		// The key is in the URL, so the error must not carry it. net/url errors
		// include the full URL, which is exactly the leak spec section 35 forbids.
		return quote{}, fmt.Errorf("alpha vantage unreachable")
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return quote{}, fmt.Errorf("alpha vantage response unreadable")
	}
	if resp.StatusCode >= 300 {
		return quote{}, fmt.Errorf("alpha vantage returned status %d", resp.StatusCode)
	}

	return parseQuote(raw)
}

// parseQuote reads the GLOBAL_QUOTE shape and, more importantly, recognises the two
// bodies Alpha Vantage returns with HTTP 200 when it is not going to answer.
//
// Both arrive as a normal success. A parser that only looked for the quote object
// would read either as "no volume", and the risk vector would then report a
// participation of zero rather than UNKNOWN.
func parseQuote(raw []byte) (quote, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return quote{}, fmt.Errorf("alpha vantage returned unparseable JSON")
	}

	if info, ok := envelope["Information"]; ok {
		text := strings.ToLower(string(info))
		switch {
		case strings.Contains(text, "premium"):
			return quote{}, ErrPremiumEndpoint
		case strings.Contains(text, "rate limit") || strings.Contains(text, "requests per day"):
			return quote{}, ErrRateLimited
		default:
			return quote{}, fmt.Errorf("alpha vantage declined to answer")
		}
	}
	if _, ok := envelope["Note"]; ok {
		return quote{}, ErrRateLimited
	}
	if _, ok := envelope["Error Message"]; ok {
		return quote{}, fmt.Errorf("alpha vantage rejected the request")
	}

	body, ok := envelope["Global Quote"]
	if !ok {
		return quote{}, fmt.Errorf("alpha vantage response contained no quote")
	}

	var fields map[string]string
	if err := json.Unmarshal(body, &fields); err != nil {
		return quote{}, fmt.Errorf("alpha vantage quote was not the expected shape")
	}
	if len(fields) == 0 {
		// An empty quote object is what an unknown symbol returns. It is not zero
		// volume; it is no answer.
		return quote{}, fmt.Errorf("alpha vantage has no quote for that symbol")
	}

	price, priceErr := strconv.ParseFloat(fields["05. price"], 64)
	volume, volumeErr := strconv.ParseFloat(fields["06. volume"], 64)
	if priceErr != nil || volumeErr != nil {
		return quote{}, fmt.Errorf("alpha vantage quote had no usable price or volume")
	}
	if price <= 0 || volume <= 0 {
		return quote{}, fmt.Errorf("alpha vantage reported a non-positive price or volume")
	}

	return quote{
		symbol:     fields["01. symbol"],
		price:      price,
		volume:     volume,
		tradingDay: fields["07. latest trading day"],
	}, nil
}

// Budget reports requests used and available today, so an operator can see why P
// went UNKNOWN without reading logs.
func (a *Adapter) Budget() (used, limit int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.spent, a.cfg.DailyRequestBudget
}
