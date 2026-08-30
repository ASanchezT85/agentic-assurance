package money

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Quantity is a count of instrument units, exactly.
//
// Separate from Amount because they are different things measured in different units and
// at different scales. Money is counted in ten-thousandths of a currency unit; a quantity
// is a count of shares, and venues accept fractional ones — Alpaca quotes fractional
// equity to nine decimal places. Sharing one type would force either money to carry a
// share's precision or a share to be rounded to money's.
//
// Eight decimal places, in an int64. Supported magnitudes reach about 46 billion units
// rather than the 92 billion an int64 would hold, for the same reason as Amount: the
// parser refuses a whole part above 2^62 divided by the scale so the conversion cannot
// overflow. A position larger than that is not a rounding problem.
type Quantity int64

// QuantityScale is the number of decimal places a quantity carries.
const QuantityScale = 8

const unitsPerQuantity = 100_000_000

// ErrQuantityPrecision is returned when a quantity carries more precision than the
// platform keeps. Refused rather than rounded, for the same reason an amount is: a
// silently rounded order is a different order, and only the caller can say which one
// they meant.
var ErrQuantityPrecision = errors.New("more precision than the supported quantity scale of eight decimal places")

// ParseQuantity reads an exact quantity from decimal text.
func ParseQuantity(s string) (Quantity, error) {
	units, err := parseFixed(s, QuantityScale, ErrQuantityPrecision)
	if err != nil {
		return 0, err
	}
	return Quantity(units), nil
}

// MustParseQuantity is ParseQuantity for constants and tests.
func MustParseQuantity(s string) Quantity {
	q, err := ParseQuantity(s)
	if err != nil {
		panic(err)
	}
	return q
}

// QuantityFromFloat exists for the edges where a float is what arrives — a simulation, an
// analytical figure. It is not on the authorization path.
func QuantityFromFloat(f float64) (Quantity, error) {
	return ParseQuantity(strconv.FormatFloat(f, 'f', -1, 64))
}

// Float is the value as a float, for display and analytics. Never for a decision.
func (q Quantity) Float() float64 { return float64(q) / unitsPerQuantity }

// String renders the quantity without trailing zeros, because a share count reads as a
// share count: "10" rather than "10.00000000". The decimal value is unchanged either way.
func (q Quantity) String() string {
	negative := q < 0
	if negative {
		q = -q
	}
	whole := int64(q) / unitsPerQuantity
	fraction := int64(q) % unitsPerQuantity

	sign := ""
	if negative {
		sign = "-"
	}
	if fraction == 0 {
		return fmt.Sprintf("%s%d", sign, whole)
	}
	digits := strings.TrimRight(fmt.Sprintf("%08d", fraction), "0")
	return fmt.Sprintf("%s%d.%s", sign, whole, digits)
}

// IsZero reports whether the quantity is exactly zero.
func (q Quantity) IsZero() bool { return q == 0 }

// MarshalJSON writes the quantity as a JSON number.
func (q Quantity) MarshalJSON() ([]byte, error) { return []byte(q.String()), nil }

// UnmarshalJSON reads the decimal literal, refusing anything beyond the scale.
//
// The literal, not a float. This is the boundary the whole type exists for: once a share
// count has been through binary64 it is whatever binary64 made of it.
func (q *Quantity) UnmarshalJSON(data []byte) error {
	text, err := numericText(data)
	if err != nil {
		return err
	}
	if text == "" {
		*q = 0
		return nil
	}
	parsed, err := ParseQuantity(text)
	if err != nil {
		return err
	}
	*q = parsed
	return nil
}

// Notional derives an order's value from a price and a quantity, exactly.
//
// This is the one place two financial numbers are multiplied, and the one place a
// rounding rule is needed: a price at four decimal places times a quantity at eight
// produces twelve, and money keeps four.
//
// The arithmetic is integer arithmetic in big.Int. The product of two int64s at these
// scales overflows an int64 for perfectly ordinary orders — a million shares at a
// thousand a share is 10^27 in twelve-decimal units — so it is computed at full width and
// reduced once.
//
// It rounds **up**, away from zero. A ceiling must count at least what an order can cost:
// rounding down would let a sequence of orders each shave a fraction off what the grant is
// charged, and the direction that errs toward refusing is the safe one for a limit. The
// rule is the same one the previous float implementation documented; what changed is that
// the value being rounded is now exact.
func NotionalOf(price Amount, quantity Quantity) Amount {
	if price == 0 || quantity == 0 {
		return 0
	}

	product := new(big.Int).Mul(big.NewInt(int64(price)), big.NewInt(int64(quantity)))
	divisor := big.NewInt(unitsPerQuantity)

	negative := product.Sign() < 0
	product.Abs(product)

	quotient, remainder := new(big.Int).QuoRem(product, divisor, new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		// Beyond what the platform can represent. Returning a wrapped value would be
		// worse than any refusal: it would be a small number standing in for an
		// enormous order. The caller treats zero as indeterminate and denies.
		return 0
	}

	value := quotient.Int64()
	if negative {
		value = -value
	}
	return Amount(value)
}

// parseFixed is the shared decimal reader for both scales.
//
// One implementation, because the rules — no exponents, no excess precision, no float
// anywhere — are the point rather than an implementation detail, and two copies would
// drift.
func parseFixed(s string, scale int, precisionErr error) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty number")
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
		return 0, fmt.Errorf("no digits")
	}
	if strings.ContainsAny(s, "eE") {
		return 0, fmt.Errorf("exponent notation is not an exact value: %q", s)
	}

	whole, fraction, hasFraction := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (hasFraction && !allDigits(fraction)) {
		return 0, fmt.Errorf("%q is not a decimal number", s)
	}
	if hasFraction && len(fraction) > scale {
		// Trailing zeros beyond the scale are not extra precision.
		if strings.Trim(fraction[scale:], "0") != "" {
			return 0, fmt.Errorf("%w: %q", precisionErr, s)
		}
		fraction = fraction[:scale]
	}

	units, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("out of range: %q", s)
	}

	multiplier := int64(1)
	for range scale {
		multiplier *= 10
	}
	if units > (1<<62)/multiplier {
		return 0, fmt.Errorf("out of range: %q", s)
	}

	padded := fraction + strings.Repeat("0", scale-len(fraction))
	var fractionUnits int64
	if padded != "" {
		fractionUnits, err = strconv.ParseInt(padded, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("out of range: %q", s)
		}
	}

	total := units*multiplier + fractionUnits
	if negative {
		total = -total
	}
	return total, nil
}

// numericText extracts the literal from a JSON token, accepting a quoted string as well.
// A client that sends a number as a string is being careful rather than wrong.
func numericText(data []byte) (string, error) {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		return "", nil
	}
	if len(text) >= 2 && text[0] == '"' && text[len(text)-1] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return "", err
		}
		return strings.TrimSpace(s), nil
	}
	return text, nil
}

// UnmarshalYAML reads a policy author's threshold as an exact decimal.
//
// Necessary rather than decorative: without it the YAML decoder would see an int64 and
// read "5000" as five thousand ten-thousandths — fifty cents where the author wrote five
// thousand. A policy that silently means 1/10,000th of what it says is worse than one
// that fails to parse.
func (a *Amount) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := Parse(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*a = parsed
	return nil
}

// MarshalYAML writes the amount as a plain decimal.
func (a Amount) MarshalYAML() (any, error) { return a.String(), nil }

// UnmarshalYAML reads an exact quantity.
func (q *Quantity) UnmarshalYAML(node *yaml.Node) error {
	parsed, err := ParseQuantity(node.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*q = parsed
	return nil
}

// MarshalYAML writes the quantity as a plain decimal.
func (q Quantity) MarshalYAML() (any, error) { return q.String(), nil }
