package security

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// INV-002: an agent can never exercise more authority than its active grant.
//
// The word doing the work is "active". A grant that has expired, has been revoked,
// or has not started yet is not a weaker grant, it is no grant. And a limit that
// cannot be evaluated is not a limit that passes.

var evalAt = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

type usageOf authority.Snapshot

func (u usageOf) Usage(context.Context, string, string, time.Time) (authority.Snapshot, error) {
	return authority.Snapshot(u), nil
}

type usageBroken struct{}

func (usageBroken) Usage(context.Context, string, string, time.Time) (authority.Snapshot, error) {
	return authority.Snapshot{}, errors.New("database unreachable")
}

// Every configured ceiling is a ceiling. One case per limit, each pushed one unit
// past the line.
func TestEveryLimitIsACeiling(t *testing.T) {
	cases := []struct {
		name  string
		limit func(*authority.Grant)
		usage authority.UsageSource
		size  float64
		code  string
	}{
		{
			name:  "per order",
			limit: func(g *authority.Grant) { g.Limits.PerOrderNotional = money.MustParse("5000") },
			size:  5000.01,
			code:  "PER_ORDER_LIMIT_EXCEEDED",
		},
		{
			name:  "rolling hour",
			limit: func(g *authority.Grant) { g.Limits.Rolling1hNotional = money.MustParse("10000") },
			usage: usageOf{Rolling1hNotional: money.MustParse("9999")},
			size:  1.01,
			code:  "ROLLING_LIMIT_EXCEEDED",
		},
		{
			name:  "daily",
			limit: func(g *authority.Grant) { g.Limits.DailyNotional = money.MustParse("15000") },
			usage: usageOf{DailyNotional: money.MustParse("14999")},
			size:  1.01,
			code:  "DAILY_LIMIT_EXCEEDED",
		},
		{
			name:  "open orders",
			limit: func(g *authority.Grant) { g.Limits.MaxOpenOrders = 3 },
			usage: usageOf{OpenOrders: 3},
			size:  1,
			code:  "MAX_OPEN_ORDERS_EXCEEDED",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := grantFor("tenant_acme")
			g.Limits = authority.Limits{}
			tc.limit(g)

			env := envelopeFor("tenant_acme")
			env.Intent.Notional = ptr(tc.size)

			got := authority.Evaluate(context.Background(), env, g, tc.usage, evalAt)
			if got.Allowed {
				t.Fatalf("an order past the %s ceiling was authorized (INV-002)", tc.name)
			}
			if got.Code != tc.code {
				t.Errorf("expected %s, got %s: %s", tc.code, got.Code, got.Reason)
			}
		})
	}
}

// Rolling limits count what was already consumed. Evaluating the new order alone
// against the ceiling would let an agent spend the limit repeatedly.
func TestRollingLimitsCountConsumedUsage(t *testing.T) {
	g := grantFor("tenant_acme")
	g.Limits = authority.Limits{Rolling1hNotional: money.MustParse("10000")}

	env := envelopeFor("tenant_acme")
	env.Intent.Notional = ptr(4000.0)

	// On its own the order is well under the ceiling.
	if got := authority.Evaluate(context.Background(), env, g, usageOf{}, evalAt); !got.Allowed {
		t.Fatalf("precondition: %s", got.Code)
	}

	// With 7,000 already spent this hour, the same order breaches it.
	got := authority.Evaluate(context.Background(), env, g, usageOf{Rolling1hNotional: money.MustParse("7000")}, evalAt)
	if got.Allowed {
		t.Fatal("the rolling limit was evaluated against the new order alone (INV-002)")
	}
	if got.Code != "ROLLING_LIMIT_EXCEEDED" {
		t.Errorf("got %s", got.Code)
	}
}

// A grant that is not active confers nothing at all, whatever its limits say.
func TestInactiveGrantConfersNothing(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(*authority.Grant)
		code   string
	}{
		{"expired", func(g *authority.Grant) { g.ValidUntil = evalAt.Add(-time.Second) }, "GRANT_EXPIRED"},
		{"not yet valid", func(g *authority.Grant) { g.ValidFrom = evalAt.Add(time.Second) }, "GRANT_NOT_YET_VALID"},
		{"revoked", func(g *authority.Grant) { g.Revoke(evalAt.Add(-time.Second), "compromise") }, "GRANT_REVOKED"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := grantFor("tenant_acme")
			tc.break_(g)

			env := envelopeFor("tenant_acme")
			env.Intent.Notional = ptr(1.0) // trivially inside every limit

			got := authority.Evaluate(context.Background(), env, g, usageOf{}, evalAt)
			if got.Allowed {
				t.Fatalf("a %s grant authorized an order (INV-002)", tc.name)
			}
			if got.Code != tc.code {
				t.Errorf("expected %s, got %s", tc.code, got.Code)
			}
			if g.Active(evalAt) {
				t.Errorf("Active reports true for a %s grant", tc.name)
			}
		})
	}
}

// A limit that cannot be read is not a limit that passes. Spec section 17: hard
// policy unavailable means DENY.
func TestUnreadableUsageDeniesRatherThanPasses(t *testing.T) {
	g := grantFor("tenant_acme")
	g.Limits = authority.Limits{DailyNotional: money.MustParse("15000")}

	got := authority.Evaluate(context.Background(), envelopeFor("tenant_acme"), g, usageBroken{}, evalAt)
	if got.Allowed {
		t.Fatal("an unreadable rolling limit was treated as satisfied (INV-002)")
	}
	if got.Code != "USAGE_UNAVAILABLE" {
		t.Errorf("got %s", got.Code)
	}
}

// An order whose size cannot be established must not satisfy a size cap. Otherwise
// the way around every notional limit is to stop stating a notional.
func TestIndeterminateSizeCannotSatisfyASizeCap(t *testing.T) {
	g := grantFor("tenant_acme")
	g.Limits = authority.Limits{PerOrderNotional: money.MustParse("5000")}

	env := envelopeFor("tenant_acme")
	env.Intent.Notional = nil
	env.Intent.Quantity = ptr(1000000.0) // a market order of unbounded value

	got := authority.Evaluate(context.Background(), env, g, usageOf{}, evalAt)
	if got.Allowed {
		t.Fatal("an order of undeterminable size passed a notional cap (INV-002)")
	}
	if got.Code != "NOTIONAL_INDETERMINATE" {
		t.Errorf("got %s", got.Code)
	}
}

// Authority is an allow-list. Anything the grant does not mention is denied, rather
// than permitted by omission.
func TestAuthorityIsAnAllowList(t *testing.T) {
	t.Run("operation not listed", func(t *testing.T) {
		g := grantFor("tenant_acme")
		g.AllowedOperations = []intent.Side{intent.SideBuy}

		env := envelopeFor("tenant_acme")
		env.Intent.Side = intent.SideSell

		if got := authority.Evaluate(context.Background(), env, g, usageOf{}, evalAt); got.Allowed {
			t.Error("SELL was authorized by a BUY-only grant (INV-002)")
		}
	})

	t.Run("asset class not listed", func(t *testing.T) {
		g := grantFor("tenant_acme")
		env := envelopeFor("tenant_acme")
		env.Intent.AssetClass = intent.AssetCrypto

		if got := authority.Evaluate(context.Background(), env, g, usageOf{}, evalAt); got.Allowed {
			t.Error("CRYPTO was authorized by an EQUITY-only grant (INV-002)")
		}
	})

	t.Run("nothing listed at all", func(t *testing.T) {
		g := grantFor("tenant_acme")
		g.AllowedOperations = nil
		g.AllowedAssetClasses = nil

		if got := authority.Evaluate(context.Background(), envelopeFor("tenant_acme"), g, usageOf{}, evalAt); got.Allowed {
			t.Error("an empty grant authorized an order (INV-002)")
		}
	})
}

// Capabilities are recorded but not enforced in V0, and the code says so out loud
// rather than leaving a field that looks enforced and is not.
func TestUnenforcedCapabilitiesAreDeclaredAsSuch(t *testing.T) {
	if got := authority.EnforcedCapabilities(); len(got) != 0 {
		t.Fatalf("EnforcedCapabilities returned %v; if a capability is now enforced, "+
			"this test and the grant documentation both need updating", got)
	}

	g := grantFor("tenant_acme")
	g.Capabilities = authority.Capabilities{MarginAllowed: false, ShortingAllowed: false}

	// A SELL under a grant that forbids shorting is still allowed today, because
	// detecting a short needs position data the platform does not have until
	// Phase 5. The test asserts the honest current behavior so that turning it on
	// is a deliberate change with a failing test to guide it.
	g.AllowedOperations = []intent.Side{intent.SideBuy, intent.SideSell}
	env := envelopeFor("tenant_acme")
	env.Intent.Side = intent.SideSell

	if got := authority.Evaluate(context.Background(), env, g, usageOf{}, evalAt); !got.Allowed {
		t.Fatalf("unexpected denial %s; capability enforcement was not supposed to be live yet", got.Code)
	}
}
