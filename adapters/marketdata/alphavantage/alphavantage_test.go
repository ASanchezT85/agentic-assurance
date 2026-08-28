package alphavantage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
)

const liveQuote = `{
    "Global Quote": {
        "01. symbol": "AAPL",
        "02. open": "310.5450",
        "03. high": "315.4000",
        "04. low": "309.4001",
        "05. price": "314.5800",
        "06. volume": "32419233",
        "07. latest trading day": "2026-08-27",
        "08. previous close": "313.4500",
        "09. change": "1.1300",
        "10. change percent": "0.3605%"
    }
}`

func session() fleet.Window {
	start := time.Date(2026, 8, 27, 13, 30, 0, 0, time.UTC)
	return fleet.Window{Start: start, End: start.Add(7 * time.Hour)}
}

func newTestAdapter(t *testing.T, handler http.HandlerFunc) (*Adapter, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	a, err := New(Config{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		SymbolFor: func(id string) (string, bool) {
			if id == "instr_us_equity_00206R102" {
				return "AAPL", true
			}
			return "", false
		},
		Now: func() time.Time { return time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return a, srv
}

// The response above is the real shape, captured from a live call.
func TestParsesTheRealQuoteShape(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(liveQuote))
	})

	notional, day, err := a.VenueVolumeE(context.Background(), "instr_us_equity_00206R102", session())
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}

	// Notional, not shares: the risk vector divides cohort notional by this.
	want := 32419233 * 314.58
	if notional != want {
		t.Errorf("notional = %v, want volume * price = %v", notional, want)
	}
	if day != "2026-08-27" {
		t.Errorf("trading day = %q", day)
	}
}

// The load-bearing honesty check. A window shorter than a session gets no answer,
// because prorating a daily total across it assumes uniform intraday volume and
// volume is heavily weighted to the open and close.
func TestShortWindowsGetNoAnswer(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was made for a window the adapter cannot answer")
	})

	start := time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC)
	for _, d := range []time.Duration{time.Second, time.Minute, time.Hour, 5 * time.Hour} {
		w := fleet.Window{Start: start, End: start.Add(d)}
		if _, _, err := a.VenueVolumeE(context.Background(), "instr_us_equity_00206R102", w); !errors.Is(err, ErrWindowTooShort) {
			t.Errorf("window of %s: error = %v, want ErrWindowTooShort", d, err)
		}
	}
}

// The two bodies Alpha Vantage returns with HTTP 200 when it will not answer. A
// parser that only looked for the quote object would read either as zero volume, and
// the risk vector would report a participation of zero rather than UNKNOWN.
func TestNonAnswersAreNotReadAsZeroVolume(t *testing.T) {
	cases := []struct {
		name string
		body string
		want error
	}{
		{
			name: "premium endpoint",
			body: `{"Information": "Thank you for using Alpha Vantage! This is a premium endpoint."}`,
			want: ErrPremiumEndpoint,
		},
		{
			name: "daily rate limit",
			body: `{"Information": "Please consider spreading out your free API requests more sparingly (1 request per second). ... 25 requests per day ..."}`,
			want: ErrRateLimited,
		},
		{
			name: "legacy note",
			body: `{"Note": "Thank you for using Alpha Vantage! Our standard API call frequency is 5 calls per minute."}`,
			want: ErrRateLimited,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			})

			notional, _, err := a.VenueVolumeE(context.Background(), "instr_us_equity_00206R102", session())
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if notional != 0 {
				t.Errorf("a non-answer produced a notional of %v", notional)
			}

			// And through the fleet.MarketData interface it must read as unknown,
			// not as zero.
			if _, ok := a.VenueVolume("instr_us_equity_00206R102", session()); ok {
				t.Error("a non-answer was reported as a usable volume (ADR-019)")
			}
		})
	}
}

// An unknown symbol returns an empty quote object. That is no answer, not zero
// volume.
func TestEmptyQuoteIsNoAnswer(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Global Quote": {}}`))
	})

	if _, ok := a.VenueVolume("instr_us_equity_00206R102", session()); ok {
		t.Error("an empty quote was reported as a usable volume")
	}
}

// An unmapped instrument never becomes a guessed symbol. Requesting volume for the
// wrong company would silently rescale P.
func TestUnmappedInstrumentIsRefused(t *testing.T) {
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("a request was made for an instrument with no symbol mapping")
	})

	if _, ok := a.VenueVolume("instr_something_unmapped", session()); ok {
		t.Error("an unmapped instrument produced a volume")
	}
}

// The daily budget is enforced here, because exceeding it at the provider returns an
// explanatory body with HTTP 200 rather than an error.
func TestDailyBudgetIsEnforcedLocally(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(liveQuote))
	}))
	t.Cleanup(srv.Close)

	// The clock advances between calls. A frozen clock would make every lookup a
	// cache hit however short the TTL, because the age of a cached entry would
	// always be zero, and the test would pass while measuring nothing.
	clock := time.Date(2026, 8, 27, 21, 0, 0, 0, time.UTC)
	a, err := New(Config{
		APIKey:             "test-key",
		BaseURL:            srv.URL,
		DailyRequestBudget: 3,
		CacheTTL:           time.Minute,
		SymbolFor:          func(string) (string, bool) { return "AAPL", true },
		Now: func() time.Time {
			clock = clock.Add(2 * time.Minute) // past the TTL every call
			return clock
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	for i := 0; i < 5; i++ {
		_, _, _ = a.VenueVolumeE(context.Background(), "instr_x", session())
	}

	if requests > 3 {
		t.Errorf("%d requests were made against a budget of 3", requests)
	}
	used, limit := a.Budget()
	if used != 3 || limit != 3 {
		t.Errorf("budget reports %d/%d", used, limit)
	}

	_, _, err = a.VenueVolumeE(context.Background(), "instr_x", session())
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("error = %v, want ErrRateLimited", err)
	}
}

// The cache is what makes 25 requests a day cover a fleet at all. The underlying
// figure is a daily total, so reuse loses nothing.
func TestQuotesAreCached(t *testing.T) {
	requests := 0
	a, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(liveQuote))
	})

	for i := 0; i < 10; i++ {
		if _, ok := a.VenueVolume("instr_us_equity_00206R102", session()); !ok {
			t.Fatalf("call %d failed", i)
		}
	}
	if requests != 1 {
		t.Errorf("%d requests for ten lookups of one daily total", requests)
	}
}

// The key is in the query string, so it must never reach an error message.
func TestTheAPIKeyNeverAppearsInAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	base := srv.URL
	srv.Close() // now unreachable, so Do() fails with a URL-bearing error

	a, err := New(Config{
		APIKey:    "super-secret-key-value",
		BaseURL:   base,
		SymbolFor: func(string) (string, bool) { return "AAPL", true },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	_, _, fetchErr := a.VenueVolumeE(context.Background(), "instr_x", session())
	if fetchErr == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(fetchErr.Error(), "super-secret-key-value") {
		t.Errorf("the API key leaked into an error message: %v (spec section 35)", fetchErr)
	}
}

func TestConstructorRefusesIncompleteConfig(t *testing.T) {
	if _, err := New(Config{SymbolFor: func(string) (string, bool) { return "", false }}); err == nil {
		t.Error("an adapter was built with no API key")
	}
	if _, err := New(Config{APIKey: "k"}); err == nil {
		t.Error("an adapter was built with no symbol resolver")
	}
}
