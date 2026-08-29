package money

import (
	"encoding/json"
	"errors"
	"math/rand"
	"testing"
)

// The arithmetic a ceiling is decided in.
func TestTheClassicDecimalCases(t *testing.T) {
	// 0.1 + 0.2 is 0.3 here. In float64 it is 0.30000000000000004, and a limit
	// comparison that lands on the boundary decides differently because of it.
	a, b := MustParse("0.1"), MustParse("0.2")
	if got := a.Add(b); got != MustParse("0.3") {
		t.Errorf("0.1 + 0.2 = %s, want 0.3000", got)
	}

	// A thousand fractional additions that must not drift.
	var total Amount
	for i := 0; i < 1000; i++ {
		total = total.Add(MustParse("0.0001"))
	}
	if total != MustParse("0.1") {
		t.Errorf("a thousand ten-thousandths = %s, want 0.1000", total)
	}
}

// Precision beyond the scale is refused, never rounded: silently rounding an input is
// how a caller's order becomes a different order.
func TestPrecisionBeyondTheScaleIsRefused(t *testing.T) {
	for _, input := range []string{"1.00001", "0.123456", "1e3", "1E-4"} {
		if _, err := Parse(input); err == nil {
			t.Errorf("%q was accepted", input)
		}
	}

	// Trailing zeros are not extra precision.
	if got := MustParse("1.10000000"); got != MustParse("1.1") {
		t.Errorf("1.10000000 parsed as %s", got)
	}
}

func TestParseRoundTrip(t *testing.T) {
	for _, input := range []string{"0", "0.0001", "1", "1200", "1200.5", "-25.25",
		"999999999.9999"} {
		parsed, err := Parse(input)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if again, err := Parse(parsed.String()); err != nil || again != parsed {
			t.Errorf("%q did not round trip: %s -> %v %v", input, parsed, again, err)
		}
	}
}

func TestJSONCarriesFullScale(t *testing.T) {
	type wrapper struct {
		Notional Amount `json:"notional"`
	}

	var w wrapper
	if err := json.Unmarshal([]byte(`{"notional": 1200}`), &w); err != nil {
		t.Fatalf("unmarshal number: %v", err)
	}
	if w.Notional != MustParse("1200") {
		t.Errorf("notional = %s", w.Notional)
	}

	if err := json.Unmarshal([]byte(`{"notional": "1200.25"}`), &w); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if w.Notional != MustParse("1200.25") {
		t.Errorf("notional = %s", w.Notional)
	}

	if err := json.Unmarshal([]byte(`{"notional": 0.00001}`), &w); !errors.Is(err, ErrPrecision) {
		t.Errorf("err = %v, want a precision refusal", err)
	}

	out, err := json.Marshal(wrapper{Notional: MustParse("1200.5")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != `{"notional":1200.5000}` {
		t.Errorf("marshalled %s", out)
	}
}

// The property the audit asked for: no combination of exact values can push the
// accumulated total past a ceiling it compared against.
func TestAccumulationNeverCrossesACeilingItComparedAgainst(t *testing.T) {
	ceiling := MustParse("10000")
	source := rand.New(rand.NewSource(7))

	for run := 0; run < 200; run++ {
		var used Amount
		for i := 0; i < 5000; i++ {
			// Any exact amount at this scale, including the awkward ones.
			candidate := Amount(source.Int63n(3_000_00))
			if used.Add(candidate) > ceiling {
				continue
			}
			used = used.Add(candidate)
		}
		if used > ceiling {
			t.Fatalf("accumulated %s past a ceiling of %s", used, ceiling)
		}
	}
}

// Boundaries: the exact limit, one unit below, one unit above.
func TestBoundariesAreExact(t *testing.T) {
	limit := MustParse("1000")

	if MustParse("1000") > limit {
		t.Error("the exact limit was treated as over")
	}
	if MustParse("999.9999") >= limit {
		t.Error("one unit below the limit was not below it")
	}
	if MustParse("1000.0001") <= limit {
		t.Error("one unit above the limit was not above it")
	}
}
