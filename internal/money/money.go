// Package money is the exact representation financial authority is decided in.
//
// Limits, notionals and thresholds were float64, and the database stored grant limits
// at scale 4 and consumed usage at scale 2. So the number a ceiling was evaluated
// against was not necessarily the number later counted against it: a reservation could
// round one way when it was authorized and another way when it was stored, and a
// ceiling that is approximately enforced is not a ceiling.
//
// One unit is 0.0001 of a currency unit. Four decimal places, the same scale as the
// database, held in an int64 — which reaches about 922 trillion, and a platform that
// needs more than that has a bigger problem than this type.
//
// Analytics may keep using floats. A risk score that is off in the twelfth decimal is
// a risk score; a ceiling that is off in the twelfth decimal has been exceeded.
package money

import (
	"errors"
	"fmt"
	"strconv"
)

// Scale is the number of decimal places every amount carries.
const Scale = 4

// unitsPerCurrency is 10^Scale.
const unitsPerCurrency = 10000

// Amount is an exact monetary value in units of 0.0001.
type Amount int64

// ErrPrecision is returned when input carries more precision than the platform keeps.
//
// Refused rather than rounded. Silently rounding an input is how a caller's order
// becomes a different order, and the caller is the only one who can decide which of
// the two they meant.
var ErrPrecision = errors.New("more precision than the supported scale of four decimal places")

// Parse reads an exact amount from decimal text.
//
// Text rather than a float, because "0.1" through a float64 is not 0.1 and the error
// is invisible until it accumulates. JSON numbers arrive as text on the wire and this
// is where they stop being text.
func Parse(s string) (Amount, error) {
	units, err := parseFixed(s, Scale, ErrPrecision)
	if err != nil {
		return 0, err
	}
	return Amount(units), nil
}

// MustParse is Parse for constants and tests.
func MustParse(s string) Amount {
	a, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return a
}

// FromFloat converts a float, refusing anything that is not exact at this scale.
//
// NOT FOR THE AUTHORIZATION PATH. A structural guard fails the build if it appears in
// internal/intent, internal/authority, internal/policy, internal/execution or
// internal/broker.
//
// The reason is that its input has already lost information. 900000000000.0002 and
// 900000000000.0003 are different amounts, both exactly representable at this scale, and
// binary64 cannot tell them apart: an agent signing the second had the first authorized.
// Converting faithfully from a float that is already wrong produces an exact record of
// the wrong number.
//
// It survives for the edges where a float is genuinely what arrives — a simulated
// figure, an analytical input — and it refuses rather than rounds for the same reason
// Parse does.
func FromFloat(f float64) (Amount, error) {
	return Parse(strconv.FormatFloat(f, 'f', -1, 64))
}

// Float is the value as a float, for analytics and display. Never for a decision.
func (a Amount) Float() float64 { return float64(a) / unitsPerCurrency }

// String renders the amount with its full scale.
func (a Amount) String() string {
	negative := a < 0
	if negative {
		a = -a
	}
	whole := int64(a) / unitsPerCurrency
	fraction := int64(a) % unitsPerCurrency
	sign := ""
	if negative {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%04d", sign, whole, fraction)
}

// Add and Sub are exact. There is no Mul or Div: a ceiling is compared and accumulated,
// never scaled, and an operation that needs rounding rules does not belong in a type
// whose job is to have none.
func (a Amount) Add(b Amount) Amount { return a + b }

// Sub subtracts exactly.
func (a Amount) Sub(b Amount) Amount { return a - b }

// IsZero reports whether the amount is exactly zero, which is what "no limit" means
// for a grant.
func (a Amount) IsZero() bool { return a == 0 }

// MarshalJSON writes the amount as a JSON number with its full scale.
//
// A number rather than a string, so an envelope stays readable to an ordinary client,
// and always with four decimals so the text a signature covers does not depend on
// whether a value happened to be round.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(a.String()), nil
}

// UnmarshalJSON reads the decimal literal, refusing anything beyond the scale.
//
// The literal rather than a float. This is the boundary the type exists for: JSON numbers
// arrive as text, and this is where they stop being text — not after a detour through
// binary64, which is where the signed amount and the authorized amount used to diverge.
func (a *Amount) UnmarshalJSON(data []byte) error {
	text, err := numericText(data)
	if err != nil {
		return err
	}
	if text == "" {
		*a = 0
		return nil
	}
	parsed, err := Parse(text)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
