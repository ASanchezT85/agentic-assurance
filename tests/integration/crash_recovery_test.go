//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/money"
)

// The crash window: the venue accepted and the platform died before writing it down.
//
// This is the one failure where doing the obvious thing loses a customer money. The
// record says PENDING, the order is live at the venue, and a restarted gateway that
// treats PENDING as "never sent" submits a second one. INV-004 is written for exactly
// this moment: an unknown outcome is not a failed outcome.
//
// Reconstructed rather than simulated with a mock. The record is claimed, the order is
// placed at the venue under the client order id the pipeline would use, and nothing
// resolves it — which is precisely the state a kill -9 between the two leaves behind.
// Then a second process picks up the same key.

func TestACrashAfterVenueAcceptanceDoesNotResubmit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	first := newE2ERig(t, now)
	ctx := context.Background()

	key := fmt.Sprintf("crash-%d", now.UnixNano())
	clientOrderID := "coid-" + key
	envelopeID := "env_" + key

	// 1. The claim commits. This is the durable boundary the pipeline crosses before
	//    it calls a venue.
	store := execution.NewPostgresStore(first.pool)
	if _, claimed, err := store.Claim(ctx, execution.Record{
		TenantID:       first.tenant,
		IdempotencyKey: key,
		EnvelopeID:     envelopeID,
		ClientOrderID:  clientOrderID,
		State:          execution.RecordPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}

	// 2. The venue accepts.
	quantity := money.MustParseQuantity("10")
	if _, err := first.broker.SubmitOrder(ctx, broker.OrderRequest{
		ClientOrderID: clientOrderID,
		Symbol:        "AAPL",
		Side:          "BUY",
		Quantity:      &quantity,
		OrderType:     "MARKET",
		TimeInForce:   "DAY",
	}); err != nil {
		t.Fatalf("the venue refused the first order: %v", err)
	}

	// 3. And nothing writes the outcome. The process is gone.
	if n := first.broker.Submissions(clientOrderID); n != 1 {
		t.Fatalf("the venue holds %d submissions before recovery, want 1", n)
	}

	// A different process, sharing only the database and the venue.
	second := first.replica(t)

	body := second.envelope(now, key, func(m map[string]any) {
		m["envelope_id"] = envelopeID
		m["correlation_id"] = "corr_" + key
		intent := m["intent"].(map[string]any)
		delete(intent, "quantity")
		delete(intent, "limit_price")
		intent["order_type"] = "MARKET"
		intent["notional"] = 1000.0
	})
	status, decoded := second.post(t, body, true)
	t.Logf("recovery returned %d: %v", status, decoded)

	// No duplicate submission. The whole point.
	if n := first.broker.Submissions(clientOrderID); n != 1 {
		t.Errorf("the venue holds %d submissions of %s after recovery. A PENDING record "+
			"means the outcome is unknown, not that nothing was sent, and the customer "+
			"now owns two positions where they authorized one (INV-004).",
			n, clientOrderID)
	}

	// The record no longer says "in flight, nobody knows". Reconciliation asked the
	// venue and wrote down what it said.
	record, err := store.Load(ctx, first.tenant, key)
	if err != nil {
		t.Fatalf("load the record: %v", err)
	}
	if record.State != execution.RecordResolved {
		t.Errorf("the record is still %s after recovery; an order live at a venue with "+
			"no recorded outcome is the state an operator cannot act on", record.State)
	}
	if record.Outcome.State == broker.StateUnknown {
		t.Errorf("the outcome is UNKNOWN although the venue could answer; reconciliation " +
			"is what turns a crash into a known state")
	}

	// And the capacity is not left held forever. A reservation that no outcome ever
	// settles is a slow leak: the grant's ceiling shrinks with every crash.
	usage := authority.NewPostgresUsage(first.pool)
	snapshot, err := usage.Usage(ctx, first.tenant, first.grantID, now)
	if err != nil {
		t.Fatalf("read usage: %v", err)
	}
	if snapshot.OpenOrders > 1 {
		t.Errorf("%d orders are counted open after one crashed submission; the "+
			"reservation was never settled", snapshot.OpenOrders)
	}
	if snapshot.Rolling1hNotional > money.MustParse("10000") {
		t.Errorf("recovery consumed %s of a 10000 ceiling for a single order",
			snapshot.Rolling1hNotional)
	}

	// The evidence says what happened rather than what was hoped. Nothing in this
	// chain may claim a submission this process did not make.
	chain, err := second.evidence.Chain(ctx, first.tenant, "corr_"+key)
	if err != nil {
		t.Fatalf("evidence chain: %v", err)
	}
	names := make([]string, 0, len(chain))
	for _, e := range chain {
		names = append(names, string(e.EventName))
		if e.EventName == evidence.SubmissionAttempted {
			t.Errorf("the recovering process recorded %s; it reconciled an existing "+
				"order and attempted nothing", e.EventName)
		}
	}
	t.Logf("recovery chain: %v", names)
}
