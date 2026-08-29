package intent

import (
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

// valid returns an envelope that passes, so each test can break exactly one thing.
func valid() AgentExecutionEnvelope {
	return AgentExecutionEnvelope{
		Signature:        Signature{Algorithm: "Ed25519", KeyID: "agent-key-01", Value: "aa"},
		SchemaVersion:    SchemaVersion,
		EnvelopeID:       "env_1",
		IdempotencyKey:   "idem_1",
		CorrelationID:    "corr_1",
		ReceivedAt:       time.Date(2026, 8, 27, 14, 32, 4, 0, time.UTC),
		TenantID:         "tenant_acme",
		AuthorityGrantID: "grant_1",
		Principal:        Principal{PrincipalID: "principal_1", AccountID: "account_1"},
		Agent: Agent{
			AgentID:     "agent_1",
			Attestation: Attestation{Level: AttestationA1},
		},
		Intent: Intent{
			AssetClass:   AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         SideBuy,
			OrderType:    OrderMarket,
			Notional:     f(4200),
			TimeInForce:  TIFDay,
		},
	}
}

func TestBaselineEnvelopeIsValid(t *testing.T) {
	e := valid()
	if err := e.Validate(); err != nil {
		t.Fatalf("baseline must be valid, got: %v", err)
	}
}

// TestSizingMatrix is the ADR-020 table, exhaustively. It is the exit criterion
// "quantity/notional XOR enforced" plus the per-order-type restriction.
func TestSizingMatrix(t *testing.T) {
	cases := []struct {
		name      string
		orderType OrderType
		notional  *float64
		quantity  *float64
		limit     *float64
		stop      *float64
		wantCode  string // empty means the combination must be accepted
	}{
		{"market with notional", OrderMarket, f(4200), nil, nil, nil, ""},
		{"market with quantity", OrderMarket, nil, f(25), nil, nil, ""},
		{"market with both", OrderMarket, f(4200), f(25), nil, nil, "SIZING_NOT_EXCLUSIVE"},
		{"market with neither", OrderMarket, nil, nil, nil, nil, "SIZING_MISSING"},

		{"limit with quantity", OrderLimit, nil, f(25), f(10), nil, ""},
		{"limit with notional", OrderLimit, f(4200), nil, f(10), nil, "NOTIONAL_NOT_ALLOWED_FOR_ORDER_TYPE"},
		{"limit with both", OrderLimit, f(4200), f(25), f(10), nil, "NOTIONAL_NOT_ALLOWED_FOR_ORDER_TYPE"},
		{"limit with neither", OrderLimit, nil, nil, f(10), nil, "QUANTITY_REQUIRED_FOR_ORDER_TYPE"},

		{"stop with quantity", OrderStop, nil, f(25), nil, f(9), ""},
		{"stop with notional", OrderStop, f(4200), nil, nil, f(9), "NOTIONAL_NOT_ALLOWED_FOR_ORDER_TYPE"},

		{"stop_limit with quantity", OrderStopLimit, nil, f(25), f(10), f(9), ""},
		{"stop_limit with notional", OrderStopLimit, f(4200), nil, f(10), f(9), "NOTIONAL_NOT_ALLOWED_FOR_ORDER_TYPE"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := valid()
			e.Intent.OrderType = tc.orderType
			e.Intent.Notional = tc.notional
			e.Intent.Quantity = tc.quantity
			e.Intent.LimitPrice = tc.limit
			e.Intent.StopPrice = tc.stop

			err := e.Validate()
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("expected accepted, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected %s, got accepted", tc.wantCode)
			}
			if !err.(ValidationErrors).Has(tc.wantCode) {
				t.Errorf("expected %s, got %v", tc.wantCode, err.(ValidationErrors).Codes())
			}
		})
	}
}

// TestVersioningWorks is the third Phase 1 exit criterion. A build implements one
// contract version and says so; it does not quietly accept a neighbouring one.
func TestVersioningWorks(t *testing.T) {
	for _, tc := range []struct {
		version string
		code    string
	}{
		{SchemaVersion, ""},
		{"", "SCHEMA_VERSION_MISSING"},
		{"0.2", "SCHEMA_VERSION_UNSUPPORTED"},
		{"1.0", "SCHEMA_VERSION_UNSUPPORTED"},
		{"0.1.0", "SCHEMA_VERSION_UNSUPPORTED"},
	} {
		e := valid()
		e.SchemaVersion = tc.version
		err := e.Validate()
		switch {
		case tc.code == "" && err != nil:
			t.Errorf("version %q: expected accepted, got %v", tc.version, err)
		case tc.code != "" && err == nil:
			t.Errorf("version %q: expected %s, got accepted", tc.version, tc.code)
		case tc.code != "" && !err.(ValidationErrors).Has(tc.code):
			t.Errorf("version %q: expected %s, got %v", tc.version, tc.code, err.(ValidationErrors).Codes())
		}
	}
}

func TestTimestampsNormalizeToUTC(t *testing.T) {
	e := valid()
	zone := time.FixedZone("UTC-4", -4*3600)
	e.ReceivedAt = time.Date(2026, 8, 27, 10, 32, 4, 0, zone)
	e.Dependencies = []Dependency{{
		Type: DependencyMarketData, ID: "feed-a",
		Verification: VerificationDeclared,
		ObservedAt:   time.Date(2026, 8, 27, 10, 32, 3, 0, zone),
	}}

	if err := e.Validate(); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if _, offset := e.ReceivedAt.Zone(); offset != 0 {
		t.Errorf("received_at kept a non-UTC offset: %d", offset)
	}
	if _, offset := e.Dependencies[0].ObservedAt.Zone(); offset != 0 {
		t.Errorf("dependency observed_at kept a non-UTC offset: %d", offset)
	}
	if got := e.ReceivedAt.Hour(); got != 14 {
		t.Errorf("received_at hour = %d, want 14 after UTC conversion", got)
	}
}

func TestAllValidationErrorsAreReportedAtOnce(t *testing.T) {
	e := valid()
	e.TenantID = ""
	e.AuthorityGrantID = ""
	e.Intent.InstrumentID = "AAPL"

	err := e.Validate()
	if err == nil {
		t.Fatal("expected rejection")
	}
	codes := err.(ValidationErrors)
	if len(codes) < 3 {
		t.Errorf("expected at least 3 errors, got %d: %v", len(codes), codes.Codes())
	}
	if !codes.Has("INSTRUMENT_ID_IS_A_TICKER") {
		t.Error("later checks were skipped after the first failure")
	}
}

func TestUnknownJSONPropertiesAreIgnored(t *testing.T) {
	raw := []byte(`{
	  "schema_version": "0.1",
	  "envelope_id": "env_1",
	  "idempotency_key": "idem_1",
	  "received_at": "2026-08-27T14:32:04Z",
	  "tenant_id": "tenant_acme",
	  "authority_grant_id": "grant_1",
	  "principal": {"principal_id": "principal_1", "account_id": "account_1"},
	  "agent": {"agent_id": "agent_1", "attestation": {"level": "A1"}},
	  "intent": {
	    "asset_class": "EQUITY", "instrument_id": "instr_x9y8z7",
	    "side": "BUY", "order_type": "MARKET", "notional": 100,
	    "time_in_force": "DAY"
	  },
	  "signature": {"algorithm": "Ed25519", "key_id": "agent-key-01", "value": "aa"},
	  "a_field_from_a_newer_producer": {"deeply": {"nested": [1, 2, 3]}}
	}`)

	if _, err := Decode(raw); err != nil {
		t.Fatalf("forward compatibility broken (ADR-008): %v", err)
	}
}

func TestMalformedJSONIsRejectedCleanly(t *testing.T) {
	_, err := Decode([]byte(`{"schema_version": `))
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !err.(ValidationErrors).Has("ENVELOPE_MALFORMED") {
		t.Errorf("expected ENVELOPE_MALFORMED, got %v", err.(ValidationErrors).Codes())
	}
}
