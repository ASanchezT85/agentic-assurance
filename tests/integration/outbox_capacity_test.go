//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"agentic-assurance/internal/evidence"
)

// T3-R001: the outbox must drain at least as fast as evidence arrives.
//
// Measured before this: arrivals of roughly 2,200 events per second under sustained load
// against a drain of 100 to 200, leaving a queue of 131,346 that took about twenty
// minutes to clear. A queue whose arrival rate exceeds its service rate diverges — it is
// not slow, it is unbounded — and the analytical plane lagged a busy period by tens of
// minutes, which is the period an incident review looks at.
//
// Three structural causes: one batch of 100 per one-second tick, so the service rate was
// a constant regardless of depth; two round trips per event to mark it published; and a
// read with no claim, so a second publisher would take the same rows.
//
// This measures the fixed path against a load at least as heavy as the platform produces.

func TestTheOutboxDrainsFasterThanEvidenceArrives(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)
	drainer := evidence.NewStore(outboxPool(t))

	conn, err := nats.Connect(streamURL(t))
	if err != nil {
		t.Skipf("no NATS at %s: %v", streamURL(t), err)
	}
	defer conn.Close()

	js, err := evidence.EnsureStream(ctx, conn)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	// A clean queue, so the measurement is of this run rather than of a backlog.
	if err := drainBacklog(ctx, drainer, js); err != nil {
		t.Fatalf("clear the backlog: %v", err)
	}

	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_cap_%d", now.UnixNano())
	store := evidence.NewStore(pool)

	// The arrival side. A sustained submission run produced about nine events per
	// decision at roughly 250 decisions per second, so this is the platform's own
	// measured output.
	const (
		batches     = 40
		perBatch    = 250
		totalEvents = batches * perBatch
	)

	// A publisher running while evidence arrives, which is how the platform runs: the
	// gateway drains continuously rather than waiting for a quiet period.
	//
	// It used to be left to whatever gateway happened to be up against the same
	// database, and the measurement then said more about what else was running than
	// about the outbox: with a gateway up the queue stayed shallow, and with none up the
	// same code reached 100% of arrivals in flight and the test failed.
	live := &evidence.OutboxPublisher{
		Store: drainer, Publisher: evidence.NewPublisher(js),
		Batch: 500, Owner: "capacity-test-concurrent",
	}
	draining, stopDraining := context.WithCancel(ctx)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for draining.Err() == nil {
			if live.Drain(draining) == 0 {
				time.Sleep(20 * time.Millisecond)
			}
		}
	}()
	defer func() { stopDraining(); <-drained }()

	arrivalStart := time.Now()
	for b := range batches {
		events := make([]evidence.Event, 0, perBatch)
		for i := range perBatch {
			at := now.Add(time.Duration(b*perBatch+i) * time.Millisecond)
			events = append(events, evidence.Event{
				SchemaVersion: evidence.SchemaVersion,
				EventID:       fmt.Sprintf("cap_%d_%d_%d", now.UnixNano(), b, i),
				EventName:     evidence.AuthorityEvaluated,
				TenantID:      tenant,
				AggregateID:   fmt.Sprintf("env_%d_%d", b, i),
				CorrelationID: fmt.Sprintf("corr_%d_%d", b, i),
				OccurredAt:    at,
				ProducedAt:    at,
				Producer:      "assurance-gateway",
				Sequence:      int64(i + 1),
				Payload:       map[string]any{"allowed": true},
			})
		}
		if err := store.AppendBatch(ctx, events); err != nil {
			t.Fatalf("append batch %d: %v", b, err)
		}
	}
	arrivalElapsed := time.Since(arrivalStart)
	arrivalRate := float64(totalEvents) / arrivalElapsed.Seconds()

	queued, oldest, err := drainer.Depth(ctx, tenant)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	t.Logf("arrival: %d events in %s (%.0f/s); queue depth %d, oldest %s",
		totalEvents, arrivalElapsed.Round(time.Millisecond), arrivalRate, queued,
		time.Since(oldest).Round(time.Millisecond))

	// The service side, measured end to end: from the first arrival to an empty queue.
	//
	// Not this publisher's share. A gateway may be running against the same database and
	// draining the same rows, and counting only what this process sent would report
	// whatever fraction of the race it won — and if the other publisher keeps up, the
	// queue is already empty when the measurement starts and the number is nonsense.
	//
	// What the acceptance property is about is whether the platform's evidence reaches
	// the bus as fast as it is produced, however many publishers do the work.
	publisher := &evidence.OutboxPublisher{
		Store:     drainer,
		Publisher: evidence.NewPublisher(js),
		Batch:     500,
		Owner:     "capacity-test",
	}

	catchUpStart := time.Now()
	var remaining int64
	for range 600 {
		publisher.Drain(ctx)
		remaining, _, err = drainer.Depth(ctx, tenant)
		if err != nil {
			t.Fatalf("depth: %v", err)
		}
		if remaining == 0 {
			break
		}
	}
	catchUp := time.Since(catchUpStart)

	t.Logf("service: peak depth during arrival %d of %d events (%.1f%%); catch-up after "+
		"the last arrival %s; depth after %d",
		queued, totalEvents, 100*float64(queued)/float64(totalEvents),
		catchUp.Round(time.Millisecond), remaining)

	if remaining != 0 {
		t.Errorf("this run's backlog did not converge to zero: %d rows remain", remaining)
	}

	// The acceptance property, stated as what can actually be measured.
	//
	// Not "service rate exceeds arrival rate" computed from the whole run: the queue
	// cannot empty before the last event arrives, so that ratio is bounded by one no
	// matter how fast the publisher is. What distinguishes a queue that keeps up from
	// one that diverges is whether depth tracks arrivals — a diverging queue's depth
	// grows with every event — and how long it takes to clear once arrivals stop.
	//
	// Before the fix, 131,346 events took about twenty minutes to clear.
	if float64(queued) > 0.25*float64(totalEvents) {
		t.Errorf("the queue reached %d of %d events in flight. Depth that tracks "+
			"arrivals is a queue whose service rate does not respond to its depth, "+
			"which is the shape that diverges.", queued, totalEvents)
	}
	if catchUp > 30*time.Second {
		t.Errorf("the backlog took %s to clear after arrivals stopped; the analytical "+
			"plane and the fleet engine are that far behind the period an incident "+
			"review needs to read", catchUp.Round(time.Second))
	}
}

// Two publishers do not take the same work.
//
// Without a claim, both select the oldest rows and publish them twice, spending exactly
// the capacity a backlog needs to clear. Duplicates are tolerable — delivery is
// at-least-once and consumers deduplicate by event id — and wasted capacity is not.
func TestTwoPublishersDoNotDrainTheSameRows(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_claim_%d", now.UnixNano())

	store := evidence.NewStore(idemPool(t))
	first := evidence.NewStore(outboxPool(t))
	second := evidence.NewStore(outboxPool(t))

	const events = 40
	batch := make([]evidence.Event, 0, events)
	for i := range events {
		at := now.Add(time.Duration(i) * time.Millisecond)
		batch = append(batch, evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("claim_%d_%d", now.UnixNano(), i),
			EventName:     evidence.AuthorityEvaluated,
			TenantID:      tenant,
			AggregateID:   "env_claim",
			CorrelationID: "corr_claim",
			OccurredAt:    at,
			ProducedAt:    at,
			Producer:      "assurance-gateway",
			Sequence:      int64(i + 1),
		})
	}
	if err := store.AppendBatch(ctx, batch); err != nil {
		t.Fatalf("append: %v", err)
	}

	at := time.Now().UTC()
	mine, err := first.Claim(ctx, 2000, "publisher-a", at, time.Minute)
	if err != nil {
		t.Fatalf("claim a: %v", err)
	}
	theirs, err := second.Claim(ctx, 2000, "publisher-b", at, time.Minute)
	if err != nil {
		t.Fatalf("claim b: %v", err)
	}

	held := map[int64]string{}
	for _, e := range mine {
		held[e.OutboxID] = "a"
	}
	overlap := 0
	for _, e := range theirs {
		if held[e.OutboxID] == "a" {
			overlap++
		}
	}
	t.Logf("publisher a claimed %d, publisher b claimed %d, overlap %d",
		len(mine), len(theirs), overlap)

	if overlap > 0 {
		t.Errorf("%d rows were claimed by both publishers. Duplicate publication is "+
			"harmless for correctness and it spends the capacity a backlog needs to "+
			"clear, which is the only capacity that matters when one exists.", overlap)
	}
	if len(mine) == 0 && len(theirs) == 0 {
		// A gateway running against the same database drains continuously and owns the
		// rows before this test asks for them. That is the lease working; it is not the
		// property under test, and the overlap check above already ran.
		t.Skip("another publisher is draining this database; nothing was left to claim")
	}
	if len(mine) == 0 {
		t.Error("the first publisher claimed nothing")
	}
}

// drainBacklog empties whatever earlier runs left, so a capacity measurement measures
// this run.
func drainBacklog(ctx context.Context, store *evidence.Store, js jetstream.JetStream) error {
	publisher := &evidence.OutboxPublisher{
		Store: store, Publisher: evidence.NewPublisher(js),
		Batch: 2000, Owner: "backlog-drain",
	}
	for range 500 {
		if publisher.Drain(ctx) == 0 {
			return nil
		}
	}
	return fmt.Errorf("the backlog did not clear")
}
