//go:build process

package process

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"agentic-assurance/adapters/alpaca"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/money"
)

// T3-B005: two gateway processes, one database, one grant.
//
// The integration suite builds a second Pipeline in the test's own process and calls it
// a replica. The separate connection pools make it a real test of database coordination
// and they do not make it two processes, and a test whose comments say "a separate
// process" while the implementation is not one is a defect in the record even when the
// property holds.
//
// These are two operating-system processes started from the built binary, each with its
// own listener, each having read its own configuration, sharing only PostgreSQL.
func TestTwoGatewayProcessesCannotOverspendOneGrant(t *testing.T) {
	ctx := context.Background()

	// 10,000 of rolling hourly notional. Six orders of 4,000 are 24,000; at most two
	// may reach a venue.
	const (
		perOrder = 4000
		orders   = 6
	)
	d := newDeployment(t, authority.Limits{
		PerOrderNotional:  money.MustParse("50000"),
		Rolling1hNotional: money.MustParse("10000"),
		DailyNotional:     money.MustParse("100000"),
		MaxOpenOrders:     50,
	})

	first := d.start(t, nil)
	second := d.start(t, nil)
	t.Logf("gateway A on %s, gateway B on %s, pid %d and %d",
		first.addr, second.addr, first.cmd.Process.Pid, second.cmd.Process.Pid)

	gateways := []*gateway{first, second}
	accepted := make([]bool, orders)

	var wg sync.WaitGroup
	for i := range orders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			g := gateways[i%len(gateways)]
			key := fmt.Sprintf("proc-%d-%d", time.Now().UnixNano(), i)
			status, _ := d.submit(t, g, key, nil)
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
	spent := money.MustParse(fmt.Sprintf("%d", allowed*perOrder))
	t.Logf("two processes, %d attempts of %d against a 10000 ceiling: %d allowed (%s)",
		orders, perOrder, allowed, spent)

	if spent > money.MustParse("10000") {
		t.Errorf("%d orders totalling %s passed a rolling ceiling of 10000 across two "+
			"operating-system processes. Each process was internally consistent and the "+
			"grant was still overspent: the limit is being enforced in a process rather "+
			"than in the database (INV-002).", allowed, spent)
	}
	if allowed == 0 {
		t.Fatal("neither process accepted anything; a ceiling that refuses everything " +
			"proves nothing about the ceiling")
	}

	// And the ledger agrees exactly with what was let through.
	usage := authority.NewPostgresUsage(d.pool)
	snapshot, err := usage.Usage(ctx, d.tenant, d.grantID, time.Now().UTC())
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if snapshot.Rolling1hNotional != spent {
		t.Errorf("the ledger records %s and the processes let through %s; a ceiling the "+
			"ledger cannot account for is one nobody can audit",
			snapshot.Rolling1hNotional, spent)
	}
}

// T3-B006: a gateway killed between a venue's acceptance and the outcome write.
//
// The integration test reconstructs that state by hand: it claims the record, submits to
// the venue and omits the outcome. That is a good test of the recovery logic and it is
// not the deployable being killed. This kills it.
//
// It needs a venue whose orders outlive the process, which a fake broker held in the
// dead process's memory cannot be. Alpaca Paper can, and it is the venue the platform
// actually talks to.
func TestAKilledGatewayDoesNotResubmit(t *testing.T) {
	if os.Getenv("ALPACA_KEY_ID") == "" {
		t.Skip("set ALPACA_BASE_URL, ALPACA_KEY_ID and ALPACA_SECRET_KEY; a venue whose " +
			"orders die with the gateway cannot show whether the gateway resubmitted")
	}
	ctx := context.Background()

	d := newDeployment(t, authority.Limits{
		PerOrderNotional:  money.MustParse("50000"),
		Rolling1hNotional: money.MustParse("100000"),
		DailyNotional:     money.MustParse("100000"),
		MaxOpenOrders:     50,
	})

	// A process armed to die at the crash point.
	crashing := d.start(t, map[string]string{
		"ASSURANCE_TEST_CRASH_POINT": "after_broker_accept_before_outcome_commit",
	})

	key := fmt.Sprintf("crash-%d", time.Now().UnixNano())
	clientOrderID := "coid-" + key

	status, body := d.submit(t, crashing, key, nil)
	t.Logf("submission to the doomed process: status=%d body=%v", status, body)

	// The process is gone. Not shut down: killed, with no deferred write, no flush and
	// no shutdown hook.
	deadline := time.Now().Add(20 * time.Second)
	for crashing.alive() && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if crashing.alive() {
		t.Fatal("the gateway did not die at the crash point")
	}
	t.Log("the gateway is gone")

	// The venue holds exactly one order, and the platform's record says PENDING.
	venue := paperVenue(t)
	if n := ordersAt(t, venue, clientOrderID); n != 1 {
		t.Fatalf("the venue holds %d orders for %s before recovery, want 1", n, clientOrderID)
	}

	store := execution.NewPostgresStore(d.pool)
	record, err := store.Load(ctx, d.tenant, key)
	if err != nil {
		t.Fatalf("load the record: %v", err)
	}
	if record.State != execution.RecordPending {
		t.Errorf("the record is %s after the crash; the window this test exists for is "+
			"the one where it is PENDING", record.State)
	}

	// A new process, not armed to crash, given the same key.
	recovered := d.start(t, nil)
	status, body = d.submit(t, recovered, key, nil)
	t.Logf("recovery: status=%d body=%v", status, body)

	if n := ordersAt(t, venue, clientOrderID); n != 1 {
		t.Errorf("the venue holds %d orders for %s after recovery. A PENDING record "+
			"means the outcome is unknown, not that nothing was sent, and the customer "+
			"now owns two positions where they authorized one (INV-004).",
			n, clientOrderID)
	}

	after, err := store.Load(ctx, d.tenant, key)
	if err != nil {
		t.Fatalf("load after recovery: %v", err)
	}
	if after.State != execution.RecordResolved {
		t.Errorf("the record is still %s after recovery; an order live at a venue with "+
			"no recorded outcome is the state an operator cannot act on", after.State)
	}

	// The reservation is settled rather than held forever.
	usage := authority.NewPostgresUsage(d.pool)
	snapshot, err := usage.Usage(ctx, d.tenant, d.grantID, time.Now().UTC())
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if snapshot.OpenOrders > 1 {
		t.Errorf("%d orders counted open after one crashed submission; the reservation "+
			"was never settled", snapshot.OpenOrders)
	}
	t.Logf("after recovery: record=%s outcome=%s open=%d consumed=%s",
		after.State, after.Outcome.State, snapshot.OpenOrders, snapshot.Rolling1hNotional)
}

// ordersAt asks the venue how many orders carry a client order id.
//
// The venue, not the platform's record of it. The whole question is whether the platform
// sent a second one, and only the venue can answer that.
func ordersAt(t *testing.T, venue broker.Adapter, clientOrderID string) int {
	t.Helper()
	ctx := context.Background()

	order, err := venue.GetOrder(ctx, clientOrderID)
	switch {
	case err == nil && order.ClientOrderID == clientOrderID:
		return 1
	case err != nil && errorsIsNotFound(err):
		return 0
	case err != nil:
		t.Fatalf("ask the venue about %s: %v", clientOrderID, err)
	}
	return 0
}

func errorsIsNotFound(err error) bool {
	return err != nil && (err == broker.ErrOrderNotFound ||
		containsAny(err.Error(), "not found", "ORDER_NOT_FOUND"))
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if len(n) > 0 && len(s) >= len(n) && indexOf(s, n) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// paperVenue is the adapter this test asks about orders, built the same way the gateway
// builds its own.
func paperVenue(t *testing.T) broker.Adapter {
	t.Helper()
	adapter, err := alpaca.New(alpaca.Config{
		BaseURL:   os.Getenv("ALPACA_BASE_URL"),
		KeyID:     os.Getenv("ALPACA_KEY_ID"),
		SecretKey: os.Getenv("ALPACA_SECRET_KEY"),
		// The same mapping the gateway was given. Instrument reference data belongs to
		// the platform, and an adapter that invented a ticker would ask the venue about
		// a different company.
		SymbolFor: func(instrumentID string) (string, bool) {
			return "AAPL", instrumentID == "instr_us_equity_00206R102"
		},
	})
	if err != nil {
		t.Fatalf("paper venue: %v", err)
	}
	return adapter
}
