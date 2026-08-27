package intent

import (
	"regexp"
	"strings"
)

// NormalizedInstrument is what policy operates on. A ticker symbol is not a
// canonical identifier (§13): the same symbol means different instruments on
// different venues, gets reused after delistings, and changes on corporate actions.
//
// Everything except InstrumentID is metadata. Policy must never key on it.
type NormalizedInstrument struct {
	InstrumentID string
	AssetClass   AssetClass
	Symbol       string
	Venue        string
	Currency     string
	FIGI         string
	ISIN         string
}

// instrumentIDPattern is the canonical shape: an "instr_" prefix followed by an
// opaque identifier. The prefix exists so that a bare ticker fed into a field that
// expects an instrument id is rejected structurally rather than silently accepted
// and treated as canonical.
var instrumentIDPattern = regexp.MustCompile(`^instr_[A-Za-z0-9][A-Za-z0-9_.:-]{2,63}$`)

// tickerPattern matches what a bare exchange ticker looks like. It exists only to
// produce a specific, actionable error for the single most likely mistake.
var tickerPattern = regexp.MustCompile(`^[A-Z]{1,5}(\.[A-Z]{1,2})?$`)

// NormalizationError explains why an instrument reference cannot be used.
//
// It is a distinct type because INV-015 forbids an invalid normalization result
// from reaching executable policy: the caller must be unable to confuse "normalized
// to something" with "could not normalize".
type NormalizationError struct {
	Input  string
	Code   string
	Reason string
}

func (e *NormalizationError) Error() string {
	return "instrument normalization failed [" + e.Code + "]: " + e.Reason + " (input: " + e.Input + ")"
}

// Normalize turns an instrument reference from an envelope into a canonical
// instrument identity.
//
// V0 requires the caller to supply an already-canonical instrument_id. There is no
// symbol-to-instrument resolution here, because resolving one needs reference data
// this phase does not have, and guessing would manufacture exactly the false
// certainty P-004 forbids. A symbol arrives as metadata or not at all.
func Normalize(instrumentID string, assetClass AssetClass, meta NormalizedInstrument) (NormalizedInstrument, error) {
	id := strings.TrimSpace(instrumentID)

	if id == "" {
		return NormalizedInstrument{}, &NormalizationError{
			Input: instrumentID, Code: "INSTRUMENT_ID_MISSING",
			Reason: "instrument_id is required; policy operates on normalized identity, not on symbols",
		}
	}
	if tickerPattern.MatchString(id) {
		return NormalizedInstrument{}, &NormalizationError{
			Input: instrumentID, Code: "INSTRUMENT_ID_IS_A_TICKER",
			Reason: "a ticker symbol is not a canonical instrument identifier (spec §13)",
		}
	}
	if !instrumentIDPattern.MatchString(id) {
		return NormalizedInstrument{}, &NormalizationError{
			Input: instrumentID, Code: "INSTRUMENT_ID_MALFORMED",
			Reason: `instrument_id must match instr_<3-64 chars of [A-Za-z0-9_.:-]>`,
		}
	}
	if !validAssetClass(assetClass) {
		return NormalizedInstrument{}, &NormalizationError{
			Input: string(assetClass), Code: "ASSET_CLASS_INVALID",
			Reason: "asset_class must be one of EQUITY, ETF, OPTION, CRYPTO",
		}
	}

	out := meta
	out.InstrumentID = id
	out.AssetClass = assetClass
	out.Symbol = strings.TrimSpace(out.Symbol)
	out.Venue = strings.TrimSpace(out.Venue)
	out.Currency = strings.ToUpper(strings.TrimSpace(out.Currency))
	return out, nil
}

func validAssetClass(a AssetClass) bool {
	switch a {
	case AssetEquity, AssetETF, AssetOption, AssetCrypto:
		return true
	}
	return false
}
