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

	publisher := &evidence.OutboxPublisher{
		Store:     drainer,
		Publisher: evidence.NewPublisher(js),
		Batch:     500,
		Owner:     "capacity-test",
	}

	// The service side, drained to empty.
	serviceStart := time.Now()
	drained := 0
	for range 200 {
		published := publisher.Drain(ctx)
		drained += published
		if published == 0 {
			break
		}
	}
	serviceElapsed := time.Since(serviceStart)
	serviceRate := float64(drained) / serviceElapsed.Seconds()

	remaining, _, err := drainer.Depth(ctx, tenant)
	if err != nil {
		t.Fatalf("depth after: %v", err)
	}

	t.Logf("service: %d events in %s (%.0f/s); catch-up %s; queue depth after %d",
		drained, serviceElapsed.Round(time.Millisecond), serviceRate,
		serviceElapsed.Round(time.Millisecond), remaining)

	if drained < totalEvents {
		t.Errorf("drained %d of %d events", drained, totalEvents)
	}
	// This run's own rows, because a shared environment always has somebody else's in
	// the queue and a measurement that counted those would be measuring the neighbours.
	if remaining != 0 {
		t.Errorf("this run's backlog did not converge to zero: %d rows remain", remaining)
	}

	// The acceptance property: steady service rate at least the steady arrival rate.
	// Both are measured on the same machine against the same database, so this compares
	// like with like rather than against a number from another run.
	if serviceRate < arrivalRate {
		t.Errorf("service rate %.0f/s is below arrival rate %.0f/s. A queue in that "+
			"relationship diverges: evidence is never lost, and the analytical plane "+
			"and the fleet engine fall progressively further behind the period an "+
			"incident review needs to read.", serviceRate, arrivalRate)
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
