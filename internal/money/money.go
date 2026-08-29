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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
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
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}

	negative := false
	switch s[0] {
	case '+':
		s = s[1:]
	case '-':
		negative = true
		s = s[1:]
	}
	if s == "" {
		return 0, fmt.Errorf("no digits in amount")
	}

	// Exponents are refused rather than expanded. "1e3" is a perfectly good number and
	// a poor way to write money, and accepting it would mean deciding what "1e-9"
	// rounds to.
	if strings.ContainsAny(s, "eE") {
		return 0, fmt.Errorf("exponent notation is not an exact amount: %q", s)
	}

	whole, fraction, hasFraction := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (hasFraction && !allDigits(fraction)) {
		return 0, fmt.Errorf("%q is not a decimal amount", s)
	}
	if hasFraction && len(fraction) > Scale {
		// Trailing zeros beyond the scale are not extra precision.
		if strings.Trim(fraction[Scale:], "0") != "" {
			return 0, fmt.Errorf("%w: %q", ErrPrecision, s)
		}
		fraction = fraction[:Scale]
	}

	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("amount out of range: %q", s)
	}

	padded := fraction + strings.Repeat("0", Scale-len(fraction))
	var fractionUnits int64
	if padded != "" {
		fractionUnits, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("amount out of range: %q", s)
		}
	}

	if units > (1<<62)/unitsPerCurrency {
		return 0, fmt.Errorf("amount out of range: %q", s)
	}

	total := units*unitsPerCurrency + fractionUnits
	if negative {
		total = -total
	}
	return Amount(total), nil
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
// It exists for the edges where a float is what arrives — an analytical figure, a
// legacy caller — and it refuses rather than rounds for the same reason Parse does.
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

// UnmarshalJSON reads decimal text, refusing anything beyond the scale.
func (a *Amount) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		*a = 0
		return nil
	}
	// A JSON string is accepted too: a client that sends money as a string is being
	// careful rather than wrong.
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		text = s
	}
	parsed, err := Parse(text)
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// Notional derives an order's value from a price and a quantity of shares.
//
// This is the one place a monetary value is not simply parsed or added, and the one
// place a rounding rule is needed: a quantity is a count of shares rather than money —
// venues accept fractional ones — so a price times a quantity can land between units.
//
// It rounds **up**, away from zero. A ceiling must count at least what an order can
// cost: rounding down would let a sequence of orders each shave a fraction off what
// the grant is charged, and the direction that errs toward refusing is the safe one
// for a limit. From here on the arithmetic is exact.
func Notional(price Amount, quantity float64) Amount {
	if price == 0 || quantity == 0 {
		return 0
	}
	units := float64(price) * quantity
	rounded := math.Ceil(math.Abs(units))
	if units < 0 {
		return Amount(-rounded)
	}
	return Amount(rounded)
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
