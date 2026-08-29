package authority

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

var now = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

// memoryUsage is the in-memory UsageSource. failWith makes the fail-closed path
// testable without breaking a database.
type memoryUsage struct {
	snapshot Snapshot
	failWith error
}

func (m memoryUsage) Usage(context.Context, string, string, time.Time) (Snapshot, error) {
	if m.failWith != nil {
		return Snapshot{}, m.failWith
	}
	return m.snapshot, nil
}

func f(v float64) *float64 { return &v }

func validGrant() *Grant {
	return &Grant{
		GrantID:             "grant_5521",
		TenantID:            "tenant_acme",
		PrincipalID:         "principal_7781",
		AccountID:           "account_4410",
		AgentID:             "agent_momentum_03",
		IssuedAt:            now.Add(-24 * time.Hour),
		ValidFrom:           now.Add(-time.Hour),
		ValidUntil:          now.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity, intent.AssetETF},
		Limits:              Limits{PerOrderNotional: money.MustParse("5000")},
		Status:              StatusActive,
	}
}

func validEnvelope() *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:    intent.SchemaVersion,
		EnvelopeID:       "env_1",
		IdempotencyKey:   "idem_1",
		ReceivedAt:       now,
		TenantID:         "tenant_acme",
		AuthorityGrantID: "grant_5521",
		Principal:        intent.Principal{PrincipalID: "principal_7781", AccountID: "account_4410"},
		Agent:            intent.Agent{AgentID: "agent_momentum_03"},
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     f(4200),
			TimeInForce:  intent.TIFDay,
		},
	}
}

// TestAuthorityMatrix is the exit criterion for Phase 3: the minimum cases of spec
// section 53, each asserting the specific denial code rather than merely "denied".
func TestAuthorityMatrix(t *testing.T) {
	cases := []struct {
		name     string
		grant    func(*Grant)
		envelope func(*intent.AgentExecutionEnvelope)
		usage    UsageSource
		wantCode string // codeAllowed means the case must be permitted
	}{
		{
			name:     "valid grant",
			wantCode: codeAllowed,
		},
		{
			name:     "expired grant",
			grant:    func(g *Grant) { g.ValidUntil = now.Add(-time.Minute) },
			wantCode: "GRANT_EXPIRED",
		},
		{
			name:     "future grant",
			grant:    func(g *Grant) { g.ValidFrom = now.Add(time.Minute) },
			wantCode: "GRANT_NOT_YET_VALID",
		},
		{
			name:     "revoked grant",
			grant:    func(g *Grant) { g.Revoke(now.Add(-time.Minute), "credential compromise") },
			wantCode: "GRANT_REVOKED",
		},
		{
			name:     "wrong agent",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Agent.AgentID = "agent_someone_else" },
			wantCode: "GRANT_WRONG_AGENT",
		},
		{
			name:     "wrong principal",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Principal.PrincipalID = "principal_other" },
			wantCode: "GRANT_WRONG_PRINCIPAL",
		},
		{
			name:     "wrong account",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Principal.AccountID = "account_other" },
			wantCode: "GRANT_WRONG_ACCOUNT",
		},
		{
			name:     "wrong tenant",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.TenantID = "tenant_other" },
			wantCode: "GRANT_WRONG_TENANT",
		},
		{
			name:     "disallowed operation",
			grant:    func(g *Grant) { g.AllowedOperations = []intent.Side{intent.SideBuy} },
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Intent.Side = intent.SideSell },
			wantCode: "OPERATION_NOT_ALLOWED",
		},
		{
			name:     "disallowed asset class",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Intent.AssetClass = intent.AssetOption },
			wantCode: "ASSET_CLASS_NOT_ALLOWED",
		},
		{
			name:     "denied instrument",
			grant:    func(g *Grant) { g.DeniedInstruments = []string{"instr_us_equity_00206R102"} },
			wantCode: "INSTRUMENT_NOT_ALLOWED",
		},
		{
			name:     "instrument outside the allow-list",
			grant:    func(g *Grant) { g.AllowedInstruments = []string{"instr_something_else"} },
			wantCode: "INSTRUMENT_NOT_ALLOWED",
		},
		{
			name:     "exceeds per-order limit",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Intent.Notional = f(5000.01) },
			wantCode: "PER_ORDER_LIMIT_EXCEEDED",
		},
		{
			name:     "exactly at the per-order limit",
			envelope: func(e *intent.AgentExecutionEnvelope) { e.Intent.Notional = f(5000) },
			wantCode: codeAllowed,
		},
		{
			name:     "exceeds rolling limit",
			grant:    func(g *Grant) { g.Limits.Rolling1hNotional = money.MustParse("10000") },
			usage:    memoryUsage{snapshot: Snapshot{Rolling1hNotional: money.MustParse("6000")}},
			wantCode: "ROLLING_LIMIT_EXCEEDED",
		},
		{
			name:     "within the rolling limit",
			grant:    func(g *Grant) { g.Limits.Rolling1hNotional = money.MustParse("10000") },
			usage:    memoryUsage{snapshot: Snapshot{Rolling1hNotional: money.MustParse("5000")}},
			wantCode: codeAllowed,
		},
		{
			name:     "exceeds daily limit",
			grant:    func(g *Grant) { g.Limits.DailyNotional = money.MustParse("15000") },
			usage:    memoryUsage{snapshot: Snapshot{DailyNotional: money.MustParse("11000")}},
			wantCode: "DAILY_LIMIT_EXCEEDED",
		},
		{
			name:     "exceeds max open orders",
			grant:    func(g *Grant) { g.Limits.MaxOpenOrders = 10 },
			usage:    memoryUsage{snapshot: Snapshot{OpenOrders: 10}},
			wantCode: "MAX_OPEN_ORDERS_EXCEEDED",
		},
		{
			name:     "usage unreadable fails closed",
			grant:    func(g *Grant) { g.Limits.DailyNotional = money.MustParse("15000") },
			usage:    memoryUsage{failWith: errors.New("connection refused")},
			wantCode: "USAGE_UNAVAILABLE",
		},
		{
			name:     "rolling limit with no usage source fails closed",
			grant:    func(g *Grant) { g.Limits.Rolling1hNotional = money.MustParse("10000") },
			usage:    nil,
			wantCode: "USAGE_UNAVAILABLE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := validGrant()
			if tc.grant != nil {
				tc.grant(g)
			}
			env := validEnvelope()
			if tc.envelope != nil {
				tc.envelope(env)
			}

			got := Evaluate(context.Background(), env, g, tc.usage, now)

			if tc.wantCode == codeAllowed {
				if !got.Allowed {
					t.Fatalf("expected allowed, denied with %s: %s", got.Code, got.Reason)
				}
				return
			}
			if got.Allowed {
				t.Fatalf("expected %s, was allowed", tc.wantCode)
			}
			if got.Code != tc.wantCode {
				t.Errorf("expected %s, got %s: %s", tc.wantCode, got.Code, got.Reason)
			}
		})
	}
}

// A missing grant is a denial, never a pass. The tempting bug is treating "no grant
// found" as "no restrictions".
func TestMissingGrantIsDenied(t *testing.T) {
	got := Evaluate(context.Background(), validEnvelope(), nil, nil, now)
	if got.Allowed {
		t.Fatal("a missing grant was treated as unrestricted")
	}
	if got.Code != "GRANT_NOT_FOUND" {
		t.Errorf("got %s", got.Code)
	}
}

// Handing the evaluator a grant other than the one the envelope names must not
// silently substitute it.
func TestGrantMismatchIsDenied(t *testing.T) {
	g := validGrant()
	g.GrantID = "grant_a_different_one"

	got := Evaluate(context.Background(), validEnvelope(), g, nil, now)
	if got.Allowed || got.Code != "GRANT_MISMATCH" {
		t.Errorf("expected GRANT_MISMATCH, got allowed=%v code=%s", got.Allowed, got.Code)
	}
}

// Revocation must take effect immediately, not at the next validity boundary.
func TestRevocationTakesEffectImmediately(t *testing.T) {
	g := validGrant()
	env := validEnvelope()

	if got := Evaluate(context.Background(), env, g, nil, now); !got.Allowed {
		t.Fatalf("precondition: %s", got.Code)
	}

	g.Revoke(now, "operator halt")
	got := Evaluate(context.Background(), env, g, nil, now)
	if got.Allowed {
		t.Fatal("a revoked grant still authorized an order")
	}
	if got.Code != "GRANT_REVOKED" {
		t.Errorf("got %s", got.Code)
	}
	if g.RevokedAt == nil {
		t.Error("revocation time not recorded")
	}
	if g.RevocationReason == "" {
		t.Error("revocation reason not recorded; spec section 36 audits human actions")
	}
}

// Lifecycle failures outrank limit arithmetic. A revoked grant must never produce a
// "limit exceeded" message that implies it would otherwise have been fine.
func TestLifecycleFailuresOutrankLimits(t *testing.T) {
	g := validGrant()
	g.Revoke(now.Add(-time.Minute), "compromise")
	env := validEnvelope()
	env.Intent.Notional = f(999999)

	got := Evaluate(context.Background(), env, g, nil, now)
	if got.Code != "GRANT_REVOKED" {
		t.Errorf("expected GRANT_REVOKED to win over the limit breach, got %s", got.Code)
	}
}

// Spec section 53, last row: enforcement works with the intelligence cloud gone.
// Evaluation takes a context, a grant and a usage source, and reaches nothing else.
func TestEvaluationHasNoCloudDependency(t *testing.T) {
	g := validGrant() // per-order limit only: no usage source needed
	got := Evaluate(context.Background(), validEnvelope(), g, nil, now)
	if !got.Allowed {
		t.Fatalf("local enforcement failed without any external dependency: %s", got.Code)
	}
}

func TestEffectiveNotional(t *testing.T) {
	cases := []struct {
		name         string
		in           intent.Intent
		want         money.Amount
		determinable bool
	}{
		{"explicit notional", intent.Intent{OrderType: intent.OrderMarket, Notional: f(4200)}, money.MustParse("4200"), true},
		{"market sized by quantity", intent.Intent{OrderType: intent.OrderMarket, Quantity: f(25)}, 0, false},
		{"limit bounded by price", intent.Intent{OrderType: intent.OrderLimit, Quantity: f(25), LimitPrice: f(100)}, money.MustParse("2500"), true},
		{"stop limit bounded by price", intent.Intent{OrderType: intent.OrderStopLimit, Quantity: f(10), LimitPrice: f(50), StopPrice: f(49)}, money.MustParse("500"), true},
		{"stop is a trigger not a price", intent.Intent{OrderType: intent.OrderStop, Quantity: f(10), StopPrice: f(49)}, 0, false},
		{"nothing at all", intent.Intent{OrderType: intent.OrderMarket}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := EffectiveNotional(tc.in)
			if ok != tc.determinable {
				t.Fatalf("determinable = %v, want %v", ok, tc.determinable)
			}
			if ok && got != tc.want {
				t.Errorf("notional = %v, want %v", got, tc.want)
			}
		})
	}
}

// A grant that caps notional must not be satisfied by an order whose notional cannot
// be established. That is the same as having no cap.
func TestIndeterminateNotionalFailsClosedAgainstACap(t *testing.T) {
	env := validEnvelope()
	env.Intent.Notional = nil
	env.Intent.Quantity = f(25) // market order sized by quantity

	got := Evaluate(context.Background(), env, validGrant(), nil, now)
	if got.Allowed {
		t.Fatal("an order of indeterminate size passed a notional cap")
	}
	if got.Code != "NOTIONAL_INDETERMINATE" {
		t.Errorf("got %s", got.Code)
	}
}

// The same order is fine when the grant caps nothing by notional.
func TestIndeterminateNotionalIsFineWithoutANotionalCap(t *testing.T) {
	g := validGrant()
	g.Limits = Limits{}
	env := validEnvelope()
	env.Intent.Notional = nil
	env.Intent.Quantity = f(25)

	if got := Evaluate(context.Background(), env, g, nil, now); !got.Allowed {
		t.Fatalf("denied with %s: %s", got.Code, got.Reason)
	}
}

// An empty allow-list authorizes nothing. An operation nobody wrote down is an
// operation nobody authorized.
func TestEmptyAllowListsDenyEverything(t *testing.T) {
	g := validGrant()
	g.AllowedOperations = nil
	if got := Evaluate(context.Background(), validEnvelope(), g, nil, now); got.Allowed {
		t.Error("an empty allowed_operations list authorized an order")
	}

	g = validGrant()
	g.AllowedAssetClasses = nil
	if got := Evaluate(context.Background(), validEnvelope(), g, nil, now); got.Allowed {
		t.Error("an empty allowed_asset_classes list authorized an order")
	}
}

// Deny always beats allow for instruments.
func TestDeniedInstrumentBeatsAllowList(t *testing.T) {
	g := validGrant()
	g.AllowedInstruments = []string{"instr_us_equity_00206R102"}
	g.DeniedInstruments = []string{"instr_us_equity_00206R102"}

	if got := Evaluate(context.Background(), validEnvelope(), g, nil, now); got.Allowed {
		t.Error("an instrument on both lists was allowed; deny must win")
	}
}

// Rows of the section 53 matrix that Phase 3 does not own. They are named here rather
// than omitted, so the gap is visible in test output instead of living in someone's
// memory.
// Envelope signature verification lives in internal/identity and is exercised on the
// pipeline in internal/gateway/signature_test.go: registered key, wrong agent's key,
// revoked, expired, unsupported algorithm, and every field changed after signing.
//
// This used to be a t.Skip saying the feature had no owning phase. It had one by the
// time an outside audit read it and called the skip what it was: a security invariant
// the repository had agreed not to check.

// Deterministic duplicate handling is idempotency, delivered in Phase 5 and covered by
// tests/integration/idempotency_pg_test.go and the replay tests in internal/gateway.
// The placeholder that named a future phase outlived the phase.
