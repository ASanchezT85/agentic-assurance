//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
)

// B-003: can a reservation be inherited by a different intent?
//
// The ledger is keyed by (tenant, idempotency_key) and a repeated key returned ALLOW
// without checking what it was reserved for. These are the audit's exploit paths,
// written to fail before the fix rather than after.

func reservationGrant(tenant, id string, rolling float64, at time.Time) *authority.Grant {
	return &authority.Grant{
		GrantID: id, TenantID: tenant,
		PrincipalID: "prin_res", AccountID: "acct_res", AgentID: "agent_res",
		IssuedAt: at.Add(-time.Hour), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
		Limits: authority.Limits{PerOrderNotional: 100000, Rolling1hNotional: rolling},
		Status: authority.StatusActive,
	}
}

// T-B003-A: a reservation left behind by a failure that never reached a venue must not
// authorize a different, larger intent later.
func TestAnOrphanReservationDoesNotAuthorizeADifferentIntent(t *testing.T) {
	usage := authority.NewPostgresUsage(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_orphan_%d", at.UnixNano())
	grant := reservationGrant(tenant, "grant_orphan", 10000, at)
	key := "K"

	// Intent A reserves 1,000 and never reaches a venue.
	first, err := usage.Reserve(ctx, grant, key, 1000, authority.ReservationIdentity{
		EnvelopeID: "env_A", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at)
	if err != nil {
		t.Fatalf("reserve A: %v", err)
	}
	if !first.Allowed {
		t.Fatalf("the first reservation was refused: %s", first.Code)
	}

	// Intent B reuses the key with another envelope and eight times the size.
	second, err := usage.Reserve(ctx, grant, key, 8000, authority.ReservationIdentity{
		EnvelopeID: "env_B", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at)
	if err != nil {
		t.Fatalf("reserve B: %v", err)
	}
	if second.Allowed {
		t.Errorf("a reservation of 1,000 for env_A authorized 8,000 for env_B: the "+
			"ledger describes an intent that never executed and the one that did is "+
			"invisible to it (INV-002). code=%s", second.Code)
	}
	if second.Code != "RESERVATION_KEY_REUSED" {
		t.Errorf("code = %s, want RESERVATION_KEY_REUSED", second.Code)
	}
}

// T-B003-B: the same key under a different grant must never inherit the first grant's
// capacity as the second grant's authority.
func TestAReservationIsNotInheritedAcrossGrants(t *testing.T) {
	usage := authority.NewPostgresUsage(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_cross_%d", at.UnixNano())
	key := "K"

	grantA := reservationGrant(tenant, "grant_A", 10000, at)
	grantB := reservationGrant(tenant, "grant_B", 10000, at)

	if d, err := usage.Reserve(ctx, grantA, key, 1000, authority.ReservationIdentity{
		EnvelopeID: "env_A", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at); err != nil || !d.Allowed {
		t.Fatalf("reserve under A: %v %s", err, d.Code)
	}

	second, err := usage.Reserve(ctx, grantB, key, 1000, authority.ReservationIdentity{
		EnvelopeID: "env_A", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at)
	if err != nil {
		t.Fatalf("reserve under B: %v", err)
	}
	if second.Allowed {
		t.Error("a reservation held against grant A was accepted as authority under grant B")
	}
}

// T-B003-D: an idempotency record pruned by retention must not leave a usage row that
// makes a fresh request invisible to rolling accounting.
func TestAStaleUsageRowDoesNotHideFreshUsage(t *testing.T) {
	usage := authority.NewPostgresUsage(idemPool(t))
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	tenant := fmt.Sprintf("tenant_stale_%d", now.UnixNano())
	grant := reservationGrant(tenant, "grant_stale", 10000, now)
	key := "K"

	// Months ago, under this key.
	if d, err := usage.Reserve(ctx, grant, key, 1000, authority.ReservationIdentity{
		EnvelopeID: "env_old", PrincipalID: "prin_res", AccountID: "acct_res",
	}, old); err != nil || !d.Allowed {
		t.Fatalf("historical reserve: %v %s", err, d.Code)
	}

	// The same key today, a different envelope. It must be evaluated as what it is: a
	// new request whose notional counts against the current window.
	fresh, err := usage.Reserve(ctx, grant, key, 9500, authority.ReservationIdentity{
		EnvelopeID: "env_new", PrincipalID: "prin_res", AccountID: "acct_res",
	}, now)
	if err != nil {
		t.Fatalf("fresh reserve: %v", err)
	}
	if fresh.Allowed {
		t.Error("a months-old usage row authorized a fresh 9,500 without counting it; " +
			"the new action is invisible to rolling and daily accounting")
	}
}

// T-B003-C: two concurrent requests carrying the same envelope under different keys.
//
// Both may reserve before the idempotency claim establishes one-envelope-per-intent.
// One wins; the loser's reservation must not survive as an orphan key with no
// idempotency record, because that is the row the paths above reuse.
func TestTheLosingSideOfAnEnvelopeRaceHoldsNoCapacity(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	rig := newE2ERig(t, now)

	type result struct {
		status int
		body   map[string]any
	}
	results := make([]result, 2)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("race-%d-%d", now.UnixNano(), i)
			body := rig.envelope(now, key, func(m map[string]any) {
				// One envelope id, two keys. Section 12.2: one envelope is one intent.
				m["envelope_id"] = fmt.Sprintf("env_race_%d", now.UnixNano())
			})
			status, decoded := rig.post(t, body, true)
			results[i] = result{status: status, body: decoded}
		}(i)
	}
	wg.Wait()

	accepted := 0
	for _, r := range results {
		if r.status == 200 || r.status == 202 {
			accepted++
		}
	}
	if accepted > 1 {
		t.Errorf("%d of 2 requests with one envelope id executed; section 12.2 says one "+
			"envelope is one intent", accepted)
	}

	// The loser must hold nothing. A reservation with no idempotency record behind it
	// is the orphan the identity check exists to stop being inherited — and it should
	// not exist in the first place.
	var reserved int
	if err := rig.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM authority_usage
		 WHERE tenant_id = $1 AND state = 'RESERVED'`, rig.tenant).Scan(&reserved); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reserved > accepted {
		t.Errorf("%d reservations are still held for %d accepted orders; the losing side "+
			"of the race left capacity reserved for an order that does not exist", reserved, accepted)
	}
}
