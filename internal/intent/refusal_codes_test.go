package intent

import (
	"errors"
	"strings"
	"testing"
)

// Every refusal this package can return, exercised at least once.
//
// A coverage census of the ninth audit asked a question nobody had: of the 106 refusal
// codes the platform can produce, which are executed by any test? Forty-two were never
// executed by a suite that runs without infrastructure, and seven of those belong to
// envelope validation — the boundary every order crosses.
//
// A refusal that no test has ever produced is a promise nobody has checked. It might be
// unreachable; it might carry the wrong field; it might be a code a client is told to
// handle and will never see. The way to find out is to make it happen.

func codes(t *testing.T, e AgentExecutionEnvelope) map[string]string {
	t.Helper()
	err := e.Validate()
	if err == nil {
		t.Fatal("the envelope was expected to be refused and was not")
	}
	var errs ValidationErrors
	if !errors.As(err, &errs) {
		t.Fatalf("refusal is not a ValidationErrors: %T %v", err, err)
	}
	found := map[string]string{}
	for _, e := range errs {
		found[e.Code] = e.Field
	}
	return found
}

func requireCode(t *testing.T, found map[string]string, code, field string) {
	t.Helper()
	got, ok := found[code]
	if !ok {
		keys := make([]string, 0, len(found))
		for k := range found {
			keys = append(keys, k)
		}
		t.Fatalf("%s was not returned; the refusals were %s", code, strings.Join(keys, ", "))
	}
	if got != field {
		t.Errorf("%s names field %q, expected %q; a caller fixing the wrong field is a "+
			"refusal that costs a round trip and reads as a platform fault", code, got, field)
	}
}

func TestAnUnknownOrderTypeIsRefused(t *testing.T) {
	e := valid()
	e.Intent.OrderType = OrderType("TWAP")
	requireCode(t, codes(t, e), "ORDER_TYPE_INVALID", "intent.order_type")
}

func TestAStopOrderWithoutAStopPriceIsRefused(t *testing.T) {
	e := valid()
	e.Intent.OrderType = OrderStop
	e.Intent.Notional = nil
	e.Intent.Quantity = q(10)
	requireCode(t, codes(t, e), "STOP_PRICE_REQUIRED", "intent.stop_price")
}

func TestAMarketOrderCarryingAStopPriceIsRefused(t *testing.T) {
	e := valid()
	e.Intent.StopPrice = f(101)
	requireCode(t, codes(t, e), "STOP_PRICE_NOT_ALLOWED", "intent.stop_price")
}

func TestALimitOrderCarryingNoLimitPriceIsRefused(t *testing.T) {
	e := valid()
	e.Intent.OrderType = OrderLimit
	e.Intent.Notional = nil
	e.Intent.Quantity = q(10)
	requireCode(t, codes(t, e), "LIMIT_PRICE_REQUIRED", "intent.limit_price")
}

func TestAPriceOfZeroIsRefused(t *testing.T) {
	e := valid()
	e.Intent.OrderType = OrderLimit
	e.Intent.Notional = nil
	e.Intent.Quantity = q(10)
	e.Intent.LimitPrice = f(0)
	requireCode(t, codes(t, e), "PRICE_NOT_POSITIVE", "intent.limit_price")
}

func TestAnUnknownTimeInForceIsRefused(t *testing.T) {
	e := valid()
	e.Intent.TimeInForce = TimeInForce("GTD")
	requireCode(t, codes(t, e), "TIME_IN_FORCE_INVALID", "intent.time_in_force")
}

func TestADependencyOutsideTheCatalogIsRefused(t *testing.T) {
	e := valid()
	e.Dependencies = []Dependency{{
		Type: DependencyType("ORACLE"), ID: "dep_1",
	}}
	found := codes(t, e)
	requireCode(t, found, "DEPENDENCY_TYPE_INVALID", "dependencies[0].type")
}

// An instrument id the normalizer cannot make sense of stops here, which is INV-015 at the
// boundary rather than at the venue.
func TestAnUnnormalizableInstrumentIsRefused(t *testing.T) {
	e := valid()
	e.Intent.InstrumentID = "AAPL"
	found := codes(t, e)

	if _, ok := found["INSTRUMENT_NORMALIZATION_FAILED"]; !ok {
		// The normalizer classifies most malformed ids with a code of its own; either way
		// the envelope must not pass. Both outcomes are recorded so the test says which.
		named := false
		for code := range found {
			if strings.HasPrefix(code, "INSTRUMENT_") {
				named = true
				t.Logf("refused as %s", code)
			}
		}
		if !named {
			t.Fatalf("a ticker was accepted as an instrument id; the refusals were %v", found)
		}
	}
}
