//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/money"
)

// Two gateways, one database, one grant.
//
// The single-process concurrency test proves the reservation serializes goroutines. It
// cannot prove the invariant, because two goroutines in one process could be held apart
// by anything — a mutex, a pool limit, the scheduler. A ceiling that holds only inside
// one process is not a ceiling for a deployment that runs three.
//
// The only thing these two replicas share is PostgreSQL. Whatever holds here holds
// because the database made it hold.

func TestTwoReplicasCannotOverspendOneGrant(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	first := newE2ERig(t, now)
	second := first.replica(t)

	// The grant allows 10,000 of rolling hourly notional. Six orders of 4,000 are
	// 24,000; at most two may reach a venue.
	const (
		perOrder = 4000.0
		orders   = 6
		ceiling  = 10000.0
	)

	replicas := []*e2eRig{first, second}
	var wg sync.WaitGroup
	accepted := make([]bool, orders)

	for i := range orders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Alternating, so neither replica can win by going first.
			rig := replicas[i%len(replicas)]
			key := fmt.Sprintf("replica-%d-%d", now.UnixNano(), i)
			body := rig.envelope(now, key, func(m map[string]any) {
				m["envelope_id"] = "env_" + key
				m["correlation_id"] = "corr_" + key
				intent := m["intent"].(map[string]any)
				delete(intent, "quantity")
				delete(intent, "limit_price")
				intent["order_type"] = "MARKET"
				intent["notional"] = perOrder
			})
			status, _ := rig.post(t, body, true)
			accepted[i] = status == 200 || status == 202
		}(i)
	}
	wg.Wait()

	allowed := 0
	for _, ok := range accepted {
		if ok {
			allowed++
		}
	}
	spent := float64(allowed) * perOrder
	t.Logf("two replicas, %d attempts of %.0f against a %.0f ceiling: %d allowed (%.0f)",
		orders, perOrder, ceiling, allowed, spent)

	if spent > ceiling {
		t.Errorf("%d orders totalling %.0f passed a rolling ceiling of %.0f across two "+
			"replicas. Each process was internally consistent and the grant was still "+
			"overspent: the limit is being enforced in a process rather than in the "+
			"database (INV-002).", allowed, spent, ceiling)
	}
	if allowed == 0 {
		t.Error("no replica accepted anything; a ceiling that refuses everything proves " +
			"nothing about the ceiling")
	}

	// And the ledger agrees with what was let through. A ceiling honoured by the
	// gateway while the recorded usage says something else is a ceiling nobody can
	// audit afterwards.
	usage := authority.NewPostgresUsage(first.pool)
	snapshot, err := usage.Usage(context.Background(), first.tenant, first.grantID, now)
	if err != nil {
		t.Fatalf("read consumed usage: %v", err)
	}
	if snapshot.Rolling1hNotional > money.MustParse("10000") {
		t.Errorf("recorded usage is %s against a ceiling of 10000",
			snapshot.Rolling1hNotional)
	}
	if snapshot.Rolling1hNotional != money.MustParse(fmt.Sprintf("%.0f", spent)) {
		t.Errorf("the ledger records %s and the gateway let through %.0f; a ceiling the "+
			"ledger cannot account for is one nobody can audit",
			snapshot.Rolling1hNotional, spent)
	}
}

// The B-003 reservation-identity exploit, across replicas.
//
// A reservation used to be keyed by (tenant, idempotency_key) and nothing else, so a key
// left behind by a request that never reached a venue would return ALLOW for a different
// envelope, a different grant and a different amount. Inside one process that was
// already wrong; across replicas it is how one gateway's abandoned reservation
// authorizes another gateway's unrelated order.
func TestAReservationIsNotInheritableAcrossReplicas(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 30, 0, 0, time.UTC)
	first := newE2ERig(t, now)
	second := first.replica(t)

	key := fmt.Sprintf("shared-key-%d", now.UnixNano())

	// The first replica submits and succeeds, so the key is held with a known identity.
	body := first.envelope(now, key, func(m map[string]any) {
		m["envelope_id"] = "env_" + key
		m["correlation_id"] = "corr_" + key
		intent := m["intent"].(map[string]any)
		delete(intent, "quantity")
		delete(intent, "limit_price")
		intent["order_type"] = "MARKET"
		intent["notional"] = 1000.0
	})
	if status, _ := first.post(t, body, true); status != 200 && status != 202 {
		t.Fatalf("the first submission was refused with %d", status)
	}

	// The second replica presents the same idempotency key for a different envelope and
	// a much larger order. A retry is the same request twice; this is not one.
	other := second.envelope(now, key, func(m map[string]any) {
		m["envelope_id"] = "env_other_" + key
		m["correlation_id"] = "corr_other_" + key
		intent := m["intent"].(map[string]any)
		delete(intent, "quantity")
		delete(intent, "limit_price")
		intent["order_type"] = "MARKET"
		intent["notional"] = 9000.0
	})
	status, decoded := second.post(t, other, true)
	if status == 200 || status == 202 {
		t.Fatalf("a different envelope inherited another replica's reservation and was "+
			"accepted (%d): %v", status, decoded)
	}
	t.Logf("refused with %d: %v", status, decoded["code"])
}
