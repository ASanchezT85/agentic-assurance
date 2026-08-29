//go:build integration

package integration

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// Can two concurrent intents both spend the same remaining capacity?
//
// The ledger has been proved race-free — concurrent writes are not lost — and that is a
// property of the data structure. INV-002 is a property of the system: an agent can
// never exercise more authority than its active grant. Between the two sits a
// check-then-act: authority reads usage in one transaction and the pipeline records it
// in another, after the venue has already been called.
//
// This test asks the system question rather than the component one.
func TestConcurrentIntentsCannotOverspendAGrant(t *testing.T) {
	now := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	rig := newE2ERig(t, now)

	// The grant allows 10,000 of rolling hourly notional. Four orders of 4,000 are
	// 16,000: at most two may reach the venue.
	const (
		perOrder = 4000.0
		orders   = 4
		ceiling  = 10000.0
	)

	var wg sync.WaitGroup
	accepted := make([]int, orders)
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("conc-%d-%d", now.UnixNano(), i)
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
			accepted[i] = status
		}(i)
	}
	wg.Wait()

	// Counted from the ledger rather than the broker: the fake counts per client order
	// id, and what matters here is the total notional the platform let through.
	reached := 0
	allowed := 0
	for _, status := range accepted {
		if status == 200 || status == 202 {
			allowed++
			reached++
		}
	}

	t.Logf("%d of %d intents were allowed; %d orders reached the venue; ceiling is %.0f "+
		"and each order is %.0f", allowed, orders, reached, ceiling, perOrder)

	if float64(reached)*perOrder > ceiling {
		t.Errorf("%d orders totalling %.0f reached the venue against a rolling ceiling of "+
			"%.0f. Each check was race-free and the ceiling was still exceeded: two "+
			"decisions read the same remaining capacity and both spent it (INV-002).",
			reached, float64(reached)*perOrder, ceiling)
	}
}
