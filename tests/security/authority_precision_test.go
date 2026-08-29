package security

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/money"
)

// The amount authority counts must be the amount authority authorized.
//
// Limits and notionals were float64 while the database stored grant limits at scale 4
// and consumed usage at scale 2, so the number a ceiling was evaluated against was not
// necessarily the number later counted against it. A reservation could round one way
// when it was authorized and another way when it was stored, and a ceiling that is
// approximately enforced is not a ceiling.
//
// This is the property rather than an example of it: thousands of exact values, chosen
// to land on and around each boundary, put through the same reserve-and-count path a
// submission uses. No sequence may leave more consumed than the grant permits, and none
// may be refused for capacity that is actually there.

func precisionGrant(rolling, daily, perOrder money.Amount) *authority.Grant {
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return &authority.Grant{
		GrantID:     "grant_precision",
		TenantID:    "tenant_precision",
		PrincipalID: "prin_precision",
		AccountID:   "acct_precision",
		AgentID:     "agent_precision",
		IssuedAt:    at.Add(-time.Hour),
		ValidFrom:   at.Add(-time.Hour),
		ValidUntil:  at.Add(time.Hour),
		Limits: authority.Limits{
			PerOrderNotional:  perOrder,
			Rolling1hNotional: rolling,
			DailyNotional:     daily,
			MaxOpenOrders:     1000000,
		},
		Status: authority.StatusActive,
	}
}

// nearBoundary produces exact amounts around a ceiling: on it, one unit either side, and
// values whose decimal parts are the ones a float cannot hold.
func nearBoundary(ceiling money.Amount, r *rand.Rand) money.Amount {
	switch r.IntN(6) {
	case 0:
		return ceiling
	case 1:
		return ceiling - 1 // one ten-thousandth below
	case 2:
		return ceiling + 1
	case 3:
		// 0.1 + 0.2 territory: the classic values that do not survive a float.
		return money.MustParse("0.1000").Add(money.MustParse("0.2000"))
	case 4:
		return money.Amount(r.Int64N(int64(ceiling)) + 1)
	default:
		// A value with every decimal place occupied.
		return money.Amount(r.Int64N(int64(ceiling)/2)+1)*1 + money.MustParse("0.9999")
	}
}

func TestNoSequenceOfExactAmountsCanCrossACeiling(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	// Ceilings whose decimal parts are awkward on purpose. A round 10,000 hides a
	// scale error; 10,000.0001 does not.
	ceilings := []struct{ rolling, daily, perOrder money.Amount }{
		{money.MustParse("10000"), money.MustParse("25000"), money.MustParse("4000")},
		{money.MustParse("10000.0001"), money.MustParse("25000.5555"), money.MustParse("3333.3333")},
		{money.MustParse("0.9999"), money.MustParse("1.9999"), money.MustParse("0.5000")},
		{money.MustParse("999999.9999"), money.MustParse("999999.9999"), money.MustParse("12345.6789")},
	}

	// Deterministic: a property test that cannot be re-run on the sequence that broke
	// it is a flake generator.
	r := rand.New(rand.NewPCG(0x5EED, 0xA55E))

	const sequences = 400
	for c, ceiling := range ceilings {
		for seq := range sequences {
			usage := authority.NewMemoryUsage()
			grant := precisionGrant(ceiling.rolling, ceiling.daily, ceiling.perOrder)

			var admitted money.Amount
			for i := range 12 {
				amount := nearBoundary(ceiling.perOrder, r)
				if amount <= 0 {
					continue
				}
				key := fmt.Sprintf("c%d_s%d_i%d", c, seq, i)
				who := authority.ReservationIdentity{
					EnvelopeID:  "env_" + key,
					PrincipalID: grant.PrincipalID,
					AccountID:   grant.AccountID,
				}

				decision, err := usage.Reserve(ctx, grant, key, amount, who, at)
				if err != nil {
					t.Fatalf("reserve: %v", err)
				}
				if decision.Allowed {
					admitted = admitted.Add(amount)
				}

				// The invariant, after every single reservation rather than at the
				// end: a ceiling crossed and then compensated for by a later refusal
				// was still crossed, and an order was live for it.
				if admitted > ceiling.rolling {
					t.Fatalf("ceiling %d sequence %d: %s admitted against a rolling "+
						"ceiling of %s after allowing %s. The amount counted is not the "+
						"amount authorized (INV-002).",
						c, seq, admitted, ceiling.rolling, amount)
				}
				if amount > ceiling.perOrder && decision.Allowed {
					t.Fatalf("ceiling %d sequence %d: %s was allowed against a per-order "+
						"ceiling of %s", c, seq, amount, ceiling.perOrder)
				}
			}

			// And the ledger agrees exactly — not approximately — with what was let
			// through. An off-by-one-unit drift is what a scale mismatch looks like
			// after it has been rounded twice.
			snapshot, err := usage.Usage(ctx, grant.TenantID, grant.GrantID, at)
			if err != nil {
				t.Fatalf("usage: %v", err)
			}
			if snapshot.Rolling1hNotional != admitted {
				t.Fatalf("ceiling %d sequence %d: the ledger records %s and %s was "+
					"admitted. The difference is a conversion, and a ceiling nobody can "+
					"reconcile is a ceiling nobody can audit.",
					c, seq, snapshot.Rolling1hNotional, admitted)
			}
		}
	}
}

// The other direction: capacity that exists must be usable.
//
// A ceiling enforced by refusing everything is trivially safe and useless. If exact
// arithmetic drifted downward, a grant would silently shrink — the customer would see
// refusals for orders that fit, and nothing in the platform would say why.
func TestAGrantCanBeSpentToItsExactCeiling(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	ceiling := money.MustParse("10000.0001")
	grant := precisionGrant(ceiling, ceiling, ceiling)
	usage := authority.NewMemoryUsage()

	// A thousand and one ten-thousandths, then the rest, then one unit too many.
	steps := []money.Amount{
		money.MustParse("0.0001"),
		money.MustParse("10000"),
	}
	var spent money.Amount
	for i, amount := range steps {
		who := authority.ReservationIdentity{
			EnvelopeID:  fmt.Sprintf("env_exact_%d", i),
			PrincipalID: grant.PrincipalID, AccountID: grant.AccountID,
		}
		decision, err := usage.Reserve(ctx, grant, fmt.Sprintf("exact_%d", i), amount, who, at)
		if err != nil {
			t.Fatalf("reserve: %v", err)
		}
		if !decision.Allowed {
			t.Fatalf("step %d of %s was refused with %s although %s of %s is spent; the "+
				"grant is smaller than it says", i, amount, decision.Code, spent, ceiling)
		}
		spent = spent.Add(amount)
	}
	if spent != ceiling {
		t.Fatalf("spent %s of a %s ceiling; the arithmetic drifted", spent, ceiling)
	}

	// And the next ten-thousandth is refused. Exactly at the boundary, not near it.
	who := authority.ReservationIdentity{
		EnvelopeID: "env_over", PrincipalID: grant.PrincipalID, AccountID: grant.AccountID,
	}
	decision, err := usage.Reserve(ctx, grant, "over", money.MustParse("0.0001"), who, at)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if decision.Allowed {
		t.Errorf("one ten-thousandth past a fully spent ceiling was allowed")
	}
}
