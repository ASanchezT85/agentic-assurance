package security

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"

	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// The amount signed must be the amount authorized.
//
// The second remediation made authority arithmetic exact and left the wire boundary
// alone: the envelope decoded financial values into `float64` and authority converted
// them back with `money.FromFloat`. Exactness that begins after the lossy step is not
// exactness — it is a precise record of whatever the float happened to become.
//
// The signature covers the raw decimal token. Authorization evaluated a binary
// approximation of it. For a platform whose promise is attributable and bounded
// autonomous financial action, those must be the same number.

// twoDecimalsOneFloat are distinct amounts that collide on a single float64. Both are
// inside the platform's supported range and both are exactly representable at scale 4;
// binary64 simply cannot tell them apart.
const (
	lowerAmount = "900000000000.0002"
	upperAmount = "900000000000.0003"
)

// signedEnvelopeWithNotional builds a signed envelope carrying one notional literal.
//
// Signed, because that is the point: the signature covers the decimal token, and what
// must reach the decision is that token rather than a binary approximation of it.
func signedEnvelopeWithNotional(t *testing.T, literal string) []byte {
	t.Helper()
	raw := envelopeWithNotional(literal)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	value, err := identity.SignEnvelope(raw, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// json.Number, not the default. Decoding into map[string]any turns every number
	// into a float64, so re-encoding would write back whatever binary64 made of the
	// literal — the test harness would destroy the value it exists to protect, and the
	// assertion would fail for its own reason rather than the platform's. This is the
	// same defect the test is about, one layer up.
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	body["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519, "key_id": "key_exact", "value": value,
	}
	signed, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return signed
}

func envelopeWithNotional(literal string) []byte {
	return []byte(`{
		"schema_version":"0.1",
		"envelope_id":"env_exact",
		"idempotency_key":"idem_exact",
		"correlation_id":"corr_exact",
		"received_at":"2026-08-29T14:30:00Z",
		"tenant_id":"tenant_exact",
		"principal":{"principal_id":"prin_exact","account_id":"acct_exact","principal_type":"INDIVIDUAL"},
		"agent":{"agent_id":"agent_exact","agent_type":"EXECUTION","operator_id":"op_exact",
		         "attestation":{"level":"A1","method":"api_key"}},
		"authority_grant_id":"grant_exact",
		"intent":{"instrument_id":"instr_us_equity_00206R102","asset_class":"EQUITY",
		          "side":"BUY","order_type":"MARKET","notional":` + literal + `,
		          "time_in_force":"DAY"}
	}`)
}

// T3-MONEY-02: the literal an agent signed survives decoding.
//
// Two envelopes carrying different notionals must not decode to the same amount. If they
// do, one agent's signed intention has been authorized as another's.
func TestASignedAmountSurvivesDecoding(t *testing.T) {
	lower, err := intent.Decode(signedEnvelopeWithNotional(t, lowerAmount))
	if err != nil {
		t.Fatalf("decode %s: %v", lowerAmount, err)
	}
	upper, err := intent.Decode(signedEnvelopeWithNotional(t, upperAmount))
	if err != nil {
		t.Fatalf("decode %s: %v", upperAmount, err)
	}

	if lower.Intent.Notional == nil || upper.Intent.Notional == nil {
		t.Fatal("an envelope with a notional decoded without one")
	}

	if *lower.Intent.Notional == *upper.Intent.Notional {
		t.Errorf("%s and %s both decoded to %v. They are different amounts, exactly "+
			"representable at the platform's scale, and the wire boundary collapsed them "+
			"into one: the amount an agent signs is not the amount the platform "+
			"authorizes.", lowerAmount, upperAmount, *lower.Intent.Notional)
	}

	if got := lower.Intent.Notional.String(); got != lowerAmount {
		t.Errorf("decoded %s as %s", lowerAmount, got)
	}
	if got := upper.Intent.Notional.String(); got != upperAmount {
		t.Errorf("decoded %s as %s", upperAmount, got)
	}
}

// T3-MONEY-01: excess precision is refused at the boundary, not absorbed.
//
// A fifth decimal place is not a small error to be rounded away. It is a caller asking
// for an amount the platform cannot represent, and only the caller can decide which of
// the two neighbouring amounts they meant.
func TestExcessPrecisionIsRefusedAtDecode(t *testing.T) {
	_, err := intent.Decode(signedEnvelopeWithNotional(t, "100.00001"))
	if err == nil {
		t.Fatal("an envelope carrying five decimal places was accepted; the platform " +
			"keeps four, so it silently authorized a different amount than the one signed")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "PRECISION") &&
		!strings.Contains(err.Error(), "decimal places") {
		t.Errorf("refused with %q; the reason should name precision so a caller can fix "+
			"the request rather than guess", err)
	}
}

// T3-MONEY-05: the economic value that reaches a venue is the one that was signed.
//
// Serialised back out, an amount must render as the literal it came in as. A round trip
// that changes the text changes what a venue is asked to do.
func TestAnAmountRoundTripsThroughJSON(t *testing.T) {
	for _, literal := range []string{lowerAmount, upperAmount, "1200", "0.0001", "1234.5678"} {
		var amount money.Amount
		if err := json.Unmarshal([]byte(literal), &amount); err != nil {
			t.Fatalf("unmarshal %s: %v", literal, err)
		}
		encoded, err := json.Marshal(amount)
		if err != nil {
			t.Fatalf("marshal %s: %v", literal, err)
		}
		var again money.Amount
		if err := json.Unmarshal(encoded, &again); err != nil {
			t.Fatalf("re-unmarshal %s: %v", encoded, err)
		}
		if again != amount {
			t.Errorf("%s round tripped to %s", literal, again)
		}
	}
}
