package authority

import (
	"context"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// The exact boundaries, pinned.
//
// Every comparison that decides money or time is one character away from being wrong, and a
// `>` that becomes a `>=` authorizes an order the customer capped, or refuses one they
// allowed, without changing a single test that works in the middle of the range. The
// examples in this suite all sit comfortably inside their limits; nothing sat on one.
//
// This is the convention, written down where it is enforced rather than in a document:
//
//	a grant is valid over [valid_from, valid_until)   — inclusive start, exclusive end
//	a money limit is a ceiling that may be reached    — equal is allowed
//	an open-order count is a number that may not be   — equal is refused
//
// The asymmetry in the last two is deliberate and easy to get backwards: per_order_notional
// 1000 permits an order of exactly 1000, and max_open_orders 5 permits at most five open
// orders, so the sixth is refused while the fifth is not.

func boundaryGrant(now time.Time, limits Limits) *Grant {
	return &Grant{
		GrantID: "grant_b", TenantID: "tenant_b", PrincipalID: "prin_b", AccountID: "acct_b",
		AgentID: "agent_b", IssuedAt: now.Add(-time.Hour),
		ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102"},
		Limits:              limits,
		Status:              StatusActive,
	}
}

func boundaryEnvelope(g *Grant, notional string) *intent.AgentExecutionEnvelope {
	amount := money.MustParse(notional)
	return &intent.AgentExecutionEnvelope{
		TenantID:         g.TenantID,
		Principal:        intent.Principal{PrincipalID: g.PrincipalID, AccountID: g.AccountID},
		Agent:            intent.Agent{AgentID: g.AgentID},
		AuthorityGrantID: g.GrantID,
		Intent: intent.Intent{
			InstrumentID: "instr_us_equity_00206R102",
			AssetClass:   intent.AssetEquity,
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     &amount,
			TimeInForce:  intent.TIFDay,
		},
	}
}

// [valid_from, valid_until): the instant a grant begins, and the instant it stops.
func TestAGrantIsValidAtItsFirstInstantAndNotAtItsLast(t *testing.T) {
	base := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	g := boundaryGrant(base, Limits{PerOrderNotional: money.MustParse("1000")})
	env := boundaryEnvelope(g, "10")

	cases := []struct {
		name  string
		at    time.Time
		allow bool
		code  string
	}{
		{"one nanosecond before it begins", g.ValidFrom.Add(-time.Nanosecond), false, "GRANT_NOT_YET_VALID"},
		{"the instant it begins", g.ValidFrom, true, ""},
		{"one nanosecond before it ends", g.ValidUntil.Add(-time.Nanosecond), true, ""},
		{"the instant it ends", g.ValidUntil, false, "GRANT_EXPIRED"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := Evaluate(context.Background(), env, g, nil, c.at)
			if d.Allowed != c.allow {
				t.Fatalf("allowed=%v at %s (code %s); the window is [valid_from, valid_until)",
					d.Allowed, c.at.Format(time.RFC3339Nano), d.Code)
			}
			if !c.allow && d.Code != c.code {
				t.Errorf("refused with %s, expected %s", d.Code, c.code)
			}
		})
	}
}

// A money limit is a ceiling that may be reached exactly.
func TestAnOrderMayReachThePerOrderLimitExactly(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	g := boundaryGrant(now, Limits{PerOrderNotional: money.MustParse("1000")})

	if d := Evaluate(context.Background(), boundaryEnvelope(g, "1000"), g, nil, now); !d.Allowed {
		t.Errorf("an order of exactly the per-order limit was refused (%s); a ceiling of "+
			"1000 that refuses 1000 is a ceiling of 999.9999 nobody wrote down", d.Code)
	}
	// And one unit past it is not.
	if d := Evaluate(context.Background(), boundaryEnvelope(g, "1000.0001"), g, nil, now); d.Allowed {
		t.Error("an order one ten-thousandth over the per-order limit was authorized")
	}
}

// The same, for the accumulated limits, through the reservation path that actually
// authorizes.
func TestUsageMayFillTheRollingAndDailyLimitsExactly(t *testing.T) {
	limits := Limits{
		PerOrderNotional:  money.MustParse("10000"),
		Rolling1hNotional: money.MustParse("1000"),
		DailyNotional:     money.MustParse("2000"),
		MaxOpenOrders:     2,
	}

	// Exactly filling the rolling window is allowed; one unit more is not.
	consumed := Snapshot{Rolling1hNotional: money.MustParse("400"), DailyNotional: money.MustParse("400")}
	if code, _ := checkLimits(limits, consumed, money.MustParse("600")); code != "" {
		t.Errorf("filling the rolling limit exactly was refused with %s", code)
	}
	if code, _ := checkLimits(limits, consumed, money.MustParse("600.0001")); code != "ROLLING_LIMIT_EXCEEDED" {
		t.Errorf("one unit over the rolling limit was refused with %q", code)
	}

	// The daily limit, with the rolling one out of the way.
	daily := Snapshot{Rolling1hNotional: 0, DailyNotional: money.MustParse("1500")}
	if code, _ := checkLimits(limits, daily, money.MustParse("500")); code != "" {
		t.Errorf("filling the daily limit exactly was refused with %s", code)
	}
	if code, _ := checkLimits(limits, daily, money.MustParse("500.0001")); code != "DAILY_LIMIT_EXCEEDED" {
		t.Errorf("one unit over the daily limit was refused with %q", code)
	}
}

// An open-order count is the other convention: equal is already too many.
func TestTheOpenOrderCountIsExclusive(t *testing.T) {
	limits := Limits{MaxOpenOrders: 2}

	if code, _ := checkLimits(limits, Snapshot{OpenOrders: 1}, money.MustParse("1")); code != "" {
		t.Errorf("a second open order was refused with %s", code)
	}
	if code, _ := checkLimits(limits, Snapshot{OpenOrders: 2}, money.MustParse("1")); code != "MAX_OPEN_ORDERS_EXCEEDED" {
		t.Errorf("a third open order against a limit of two was refused with %q; "+
			"max_open_orders 2 means at most two are open at once", code)
	}
}

// A limit of zero is "no limit", and that is the one reading that must never drift: a
// grant with no rolling cap must not become a grant that permits nothing.
func TestAZeroLimitMeansNoLimit(t *testing.T) {
	limits := Limits{PerOrderNotional: 0, Rolling1hNotional: 0, DailyNotional: 0, MaxOpenOrders: 0}
	huge := Snapshot{
		Rolling1hNotional: money.MustParse("900000000000"),
		DailyNotional:     money.MustParse("900000000000"),
		OpenOrders:        10000,
	}
	if code, _ := checkLimits(limits, huge, money.MustParse("900000000000")); code != "" {
		t.Errorf("a grant with no limits refused an order with %s", code)
	}
}

// The rolling window's own edge: an entry exactly one hour old is outside it.
func TestTheRollingWindowExcludesTheHourMark(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	usage := NewMemoryUsage()
	g := boundaryGrant(now, Limits{
		PerOrderNotional: money.MustParse("10000"), Rolling1hNotional: money.MustParse("1000"),
	})

	// One reservation exactly an hour before the instant we ask about.
	who := ReservationIdentity{EnvelopeID: "env_old", PrincipalID: g.PrincipalID, AccountID: g.AccountID}
	if d, err := usage.Reserve(ctx, g, "key_old", money.MustParse("900"), who,
		now.Add(-time.Hour)); err != nil || !d.Allowed {
		t.Fatalf("the first reservation was refused: %v %s", err, d.Code)
	}

	consumed, err := usage.Usage(ctx, g.TenantID, g.GrantID, now)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if consumed.Rolling1hNotional != 0 {
		t.Errorf("an entry exactly one hour old still counts as %s in the rolling window; "+
			"the window is the last hour, open at its far end",
			consumed.Rolling1hNotional)
	}

	// A nanosecond later, it counts.
	consumed, err = usage.Usage(ctx, g.TenantID, g.GrantID, now.Add(-time.Nanosecond))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if consumed.Rolling1hNotional == 0 {
		t.Error("an entry one nanosecond inside the window was not counted")
	}
}
