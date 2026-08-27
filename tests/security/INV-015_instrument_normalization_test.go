package security

import (
	"testing"

	"agentic-assurance/internal/intent"
)

// INV-015: an invalid instrument normalization result cannot proceed to executable
// policy.
//
// The specific failure this guards is treating a ticker as canonical (spec section
// 13). Symbols are reused after delistings, mean different instruments on different
// venues, and change on corporate actions. A policy keyed on "AAPL" is a policy
// keyed on a string that does not identify anything durably.

func TestTickerIsNotAnInstrumentIdentifier(t *testing.T) {
	for _, ticker := range []string{"AAPL", "TSLA", "F", "BRK.B", "SPY"} {
		_, err := intent.Normalize(ticker, intent.AssetEquity, intent.NormalizedInstrument{})
		if err == nil {
			t.Errorf("ticker %q normalized successfully; it is not a canonical id (INV-015)", ticker)
			continue
		}
		var ne *intent.NormalizationError
		if !asNormErr(err, &ne) || ne.Code != "INSTRUMENT_ID_IS_A_TICKER" {
			t.Errorf("ticker %q: wrong reason: %v", ticker, err)
		}
	}
}

func TestUnnormalizableInstrumentBlocksTheEnvelope(t *testing.T) {
	cases := []struct {
		name  string
		id    string
		class intent.AssetClass
		code  string
	}{
		{"empty", "", intent.AssetEquity, "INSTRUMENT_ID_MISSING"},
		{"ticker", "AAPL", intent.AssetEquity, "INSTRUMENT_ID_IS_A_TICKER"},
		{"no prefix", "us-equity-00206R102", intent.AssetEquity, "INSTRUMENT_ID_MALFORMED"},
		{"prefix only", "instr_", intent.AssetEquity, "INSTRUMENT_ID_MALFORMED"},
		{"too short", "instr_ab", intent.AssetEquity, "INSTRUMENT_ID_MALFORMED"},
		{"whitespace", "   ", intent.AssetEquity, "INSTRUMENT_ID_MISSING"},
		{"bad asset class", "instr_us_equity_00206R102", intent.AssetClass("STONKS"), "ASSET_CLASS_INVALID"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := baseEnvelope()
			e.Intent.InstrumentID = tc.id
			e.Intent.AssetClass = tc.class

			err := e.Validate()
			if err == nil {
				t.Fatalf("envelope with instrument %q was accepted (INV-015)", tc.id)
			}
			if !err.(intent.ValidationErrors).Has(tc.code) {
				t.Errorf("expected %s, got %v", tc.code, err.(intent.ValidationErrors).Codes())
			}
		})
	}
}

// A failed normalization must return no usable instrument. A caller that ignores
// the error must not find a half-populated struct waiting for it.
func TestFailedNormalizationReturnsNothingUsable(t *testing.T) {
	got, err := intent.Normalize("AAPL", intent.AssetEquity, intent.NormalizedInstrument{
		Symbol: "AAPL", Venue: "XNAS", Currency: "usd",
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	if got.InstrumentID != "" || got.Symbol != "" || got.Venue != "" {
		t.Errorf("failed normalization leaked a partially populated instrument: %+v", got)
	}
}

func TestSuccessfulNormalizationKeepsMetadataAndCanonicalisesCurrency(t *testing.T) {
	got, err := intent.Normalize("instr_us_equity_00206R102", intent.AssetEquity,
		intent.NormalizedInstrument{Symbol: " AAPL ", Venue: " XNAS ", Currency: "usd", FIGI: "BBG000B9XRY4"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if got.InstrumentID != "instr_us_equity_00206R102" {
		t.Errorf("instrument_id = %q", got.InstrumentID)
	}
	if got.Symbol != "AAPL" || got.Venue != "XNAS" {
		t.Errorf("metadata not trimmed: %+v", got)
	}
	if got.Currency != "USD" {
		t.Errorf("currency = %q, want USD", got.Currency)
	}
	if got.FIGI != "BBG000B9XRY4" {
		t.Errorf("FIGI dropped: %+v", got)
	}
}

func asNormErr(err error, target **intent.NormalizationError) bool {
	ne, ok := err.(*intent.NormalizationError)
	if ok {
		*target = ne
	}
	return ok
}
