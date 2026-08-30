//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
)

// F4-B003: a message is not acknowledged until the insert it stands for has happened.
//
// The consumer appended rows to a slice inside a per-event handler that returned nil, so
// Fetch acknowledged every message in the batch, and the ClickHouse insert ran after the
// fetch returned. A failed insert therefore had nothing left to redeliver. The events were
// safe in PostgreSQL — that is the record — and they never reached the analytical plane,
// which is what an incident review actually reads. A hole in the analytical copy caused by
// a store being briefly unavailable, with nothing to say so.
//
// These run against real JetStream, with an injectable sink: delivery semantics cannot be
// tested against a store that always succeeds, and mocking JetStream would test the mock.

// flakySink fails its first n insert calls, then succeeds, recording everything it was
// asked to write.
type flakySink struct {
	mu       sync.Mutex
	failures int
	calls    int
	rows     []string
}

func (s *flakySink) InsertEvidence(_ context.Context, rows ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.calls <= s.failures {
		return fmt.Errorf("the analytical store is unavailable (injected, call %d)", s.calls)
	}
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *flakySink) written() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.rows...)
}

// publishForProjection puts n unique events on the bus and returns the subject filter that
// selects exactly them.
func publishForProjection(t *testing.T, js jetstream.JetStream, tenant string, n int) []evidence.Event {
	t.Helper()
	ctx := context.Background()

	publisher := evidence.NewPublisher(js)
	events := make([]evidence.Event, 0, n)
	for i := range n {
		at := time.Now().UTC().Add(time.Duration(i) * time.Millisecond)
		e := evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("ack_%s_%d", tenant, i),
			EventName:     evidence.AuthorityEvaluated,
			TenantID:      tenant,
			AggregateID:   fmt.Sprintf("env_%d", i),
			CorrelationID: fmt.Sprintf("corr_%d", i),
			OccurredAt:    at,
			ProducedAt:    at,
			Producer:      "assurance-gateway",
			Sequence:      int64(i + 1),
			Payload:       map[string]any{"allowed": true},
		}
		if err := publisher.Publish(ctx, e); err != nil {
			t.Fatalf("publish: %v", err)
		}
		events = append(events, e)
	}
	return events
}

// F4-PROJECTION-01 and 02: a failed insert leaves the batch redeliverable, and the same
// events arrive once the store recovers.
func TestAFailedProjectionInsertLeavesMessagesRedeliverable(t *testing.T) {
	ctx := context.Background()
	js := projectionStream(t)
	tenant := fmt.Sprintf("tenant_ack_%d", time.Now().UnixNano())

	const events = 25
	published := publishForProjection(t, js, tenant, events)

	consumer, err := evidence.NewConsumer(ctx, js,
		"projection_ack_"+shortID(tenant), "evidence."+tenant+".>")
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}

	sink := &flakySink{failures: 1}
	projection := &fleet.EvidenceProjection{Sink: sink}

	// The first fetch: the insert fails, so nothing may be acknowledged.
	if _, err := consumer.FetchBatch(ctx, 500, 2*time.Second, projection.HandleBatch); err == nil {
		t.Fatal("the failed insert was reported as success")
	}
	if got := len(sink.written()); got != 0 {
		t.Fatalf("the sink recorded %d rows from a failed insert", got)
	}

	// The second fetch gets the same events, because the first batch was never
	// acknowledged. Before the fix they were acknowledged as they were decoded, and this
	// returned nothing at all.
	seen := map[string]bool{}
	deadline := time.Now().Add(30 * time.Second)
	for len(seen) < events && time.Now().Before(deadline) {
		if _, err := consumer.FetchBatch(ctx, 500, 2*time.Second,
			func(ctx context.Context, batch []evidence.Event) error {
				if err := projection.HandleBatch(ctx, batch); err != nil {
					return err
				}
				for _, e := range batch {
					seen[e.EventID] = true
				}
				return nil
			}); err != nil {
			t.Logf("fetch: %v", err)
		}
	}

	if len(seen) != events {
		t.Fatalf("%d of %d events were redelivered after the insert failed. An "+
			"acknowledgement is a promise that the side effect it stands for has "+
			"happened; acknowledging before the insert loses exactly the events the "+
			"store was unavailable for, and nothing says so.", len(seen), events)
	}

	// F4-PROJECTION-03: every event appears once in what the sink was asked to write.
	written := sink.written()
	if len(written) != events {
		t.Errorf("the sink was asked to write %d rows for %d events; a retried batch must "+
			"not multiply the analytical copy", len(written), events)
	}
	for _, e := range published {
		if !seen[e.EventID] {
			t.Errorf("%s never arrived", e.EventID)
		}
	}
}

// F4-PROJECTION-04 and 06: a consumer that restarts after a failed insert recovers the
// batch, and a successful insert acknowledges after the write.
func TestAConsumerRestartRecoversTheFailedBatch(t *testing.T) {
	ctx := context.Background()
	js := projectionStream(t)
	tenant := fmt.Sprintf("tenant_ackr_%d", time.Now().UnixNano())

	const events = 10
	publishForProjection(t, js, tenant, events)

	durable := "projection_restart_" + shortID(tenant)
	first, err := evidence.NewConsumer(ctx, js, durable, "evidence."+tenant+".>")
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}

	failing := &fleet.EvidenceProjection{Sink: &flakySink{failures: 10}}
	if _, err := first.FetchBatch(ctx, 500, 2*time.Second, failing.HandleBatch); err == nil {
		t.Fatal("the failed insert was reported as success")
	}

	// A new consumer over the same durable, as a restarted process gets.
	second, err := evidence.NewConsumer(ctx, js, durable, "evidence."+tenant+".>")
	if err != nil {
		t.Fatalf("consumer after restart: %v", err)
	}
	sink := &flakySink{}
	recovered := &fleet.EvidenceProjection{Sink: sink}

	seen := 0
	deadline := time.Now().Add(30 * time.Second)
	for seen < events && time.Now().Before(deadline) {
		n, err := second.FetchBatch(ctx, 500, 2*time.Second, recovered.HandleBatch)
		if err != nil {
			t.Logf("fetch: %v", err)
			continue
		}
		seen += n
	}

	if seen < events {
		t.Fatalf("a restarted consumer recovered %d of %d events left unacknowledged by "+
			"a failed insert", seen, events)
	}
	if got := len(sink.written()); got < events {
		t.Errorf("the sink received %d rows for %d events", got, events)
	}
}

// F4-PROJECTION-05: an unparseable message is terminated, and said out loud.
func TestAnUnparseableMessageIsTerminatedAndReported(t *testing.T) {
	ctx := context.Background()
	js := projectionStream(t)
	tenant := fmt.Sprintf("tenant_ackp_%d", time.Now().UnixNano())

	// One good event and one that will never decode.
	publishForProjection(t, js, tenant, 1)
	if _, err := js.Publish(ctx, "evidence."+tenant+".poison", []byte("{not json")); err != nil {
		t.Fatalf("publish poison: %v", err)
	}

	consumer, err := evidence.NewConsumer(ctx, js,
		"projection_poison_"+shortID(tenant), "evidence."+tenant+".>")
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	reported := 0
	consumer.Report = func(poison int) { reported += poison }

	sink := &flakySink{}
	projection := &fleet.EvidenceProjection{Sink: sink}

	deadline := time.Now().Add(20 * time.Second)
	for reported == 0 && time.Now().Before(deadline) {
		if _, err := consumer.FetchBatch(ctx, 500, 2*time.Second, projection.HandleBatch); err != nil {
			t.Logf("fetch: %v", err)
		}
	}

	if reported == 0 {
		t.Error("a message was discarded and nothing surfaced it. Terminating a message " +
			"is a decision to lose an event, and one that says nothing is " +
			"indistinguishable from a bug in the projection.")
	}
	t.Logf("terminated and reported: %d", reported)
}

// shortID keeps a durable consumer name inside what JetStream accepts.
func shortID(tenant string) string {
	if len(tenant) <= 24 {
		return tenant
	}
	return tenant[len(tenant)-24:]
}

// projectionStream connects to NATS and makes sure the stream exists.
func projectionStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	nc := connectNATS(t)
	js, err := evidence.EnsureStream(context.Background(), nc)
	if err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	return js
}
