//go:build integration

package integration

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"agentic-assurance/adapters/marketdata/alphavantage"
	"agentic-assurance/internal/fleet"
)

// A live check against Alpha Vantage.
//
// The contract tests in the adapter package prove request shaping and response
// parsing against a local server. Only this file proves the adapter works against
// the provider, and it is the difference between "the parser handles the shape we
// wrote down" and "the shape we wrote down is the shape they send".
//
// It skips without a key, so nobody is blocked by a credential they do not have, and
// the key is never read from a file in this repository.
//
// The free plan allows 25 requests a day. This test spends at most one.
//
//	ALPHAVANTAGE_API_KEY=... go test -tags=integration -run Live ./tests/integration/

func TestLiveAlphaVantageQuote(t *testing.T) {
	key := os.Getenv("ALPHAVANTAGE_API_KEY")
	if key == "" {
		t.Skip("ALPHAVANTAGE_API_KEY not set; skipping the live market data check")
	}

	a, err := alphavantage.New(alphavantage.Config{
		APIKey: key,
		SymbolFor: func(id string) (string, bool) {
			if id == "instr_us_equity_00206R102" {
				return "AAPL", true
			}
			return "", false
		},
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// A full session, because the adapter refuses anything shorter.
	start := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Hour)
	window := fleet.Window{Start: start, End: start.Add(8 * time.Hour)}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	notional, tradingDay, err := a.VenueVolumeE(ctx, "instr_us_equity_00206R102", window)
	switch {
	case errors.Is(err, alphavantage.ErrRateLimited):
		t.Skip("the free key's daily budget is spent; skipping rather than failing on a quota")
	case err != nil:
		t.Fatalf("live quote failed: %v", err)
	}

	if notional <= 0 {
		t.Fatalf("venue notional = %v", notional)
	}
	if tradingDay == "" {
		t.Error("no trading day was reported; without it the figure cannot be dated")
	}

	// A liquid US name trades in the billions of notional per session. A figure far
	// below that means shares were returned where notional was expected, which
	// would make P wrong by two orders of magnitude and still look plausible.
	if notional < 1e8 {
		t.Errorf("venue notional %.0f for a liquid name looks like a share count "+
			"rather than notional", notional)
	}

	t.Logf("live: AAPL traded %.0f notional on %s (volume * price, daily total)",
		notional, tradingDay)

	used, limit := a.Budget()
	t.Logf("free-tier budget: %d of %d requests used", used, limit)
}

// The constraint that matters more than the happy path: the free tier cannot answer
// for the windows the fleet engine actually uses.
func TestLiveAdapterRefusesShortWindows(t *testing.T) {
	key := os.Getenv("ALPHAVANTAGE_API_KEY")
	if key == "" {
		t.Skip("ALPHAVANTAGE_API_KEY not set")
	}

	a, err := alphavantage.New(alphavantage.Config{
		APIKey:    key,
		SymbolFor: func(string) (string, bool) { return "AAPL", true },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// A one-minute cohort window, which is what the fleet engine measures.
	start := time.Now().UTC().Add(-2 * time.Hour)
	window := fleet.Window{Start: start, End: start.Add(time.Minute)}

	if _, ok := a.VenueVolume("instr_us_equity_00206R102", window); ok {
		t.Error("the adapter answered for a one-minute window; the free tier reports " +
			"daily totals, and prorating one across a minute assumes uniform intraday " +
			"volume (ADR-019, P-004)")
	}
}
