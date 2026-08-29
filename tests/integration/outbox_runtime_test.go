//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
)

// streamURL is the bus this test talks to. Named apart from the existing helper in
// evidence_stream_test.go so the two files cannot disagree about the default.
func streamURL(t *testing.T) string {
	t.Helper()
	if url := os.Getenv("NATS_URL"); url != "" {
		return url
	}
	return "nats://localhost:4222"
}

// The event backbone, through the wiring the binaries use.
//
// EnsureStream, Publisher and Consumer had passing tests since Phase 6 and no binary
// ever constructed them: evidence went to PostgreSQL, telemetry went to ClickHouse, and
// the documentation called JetStream the backbone. A library round trip would have
// passed then too, which is exactly why this test starts where a submission starts —
// evidence committed through the store — and ends where an analytical reader looks.

func TestCommittedEvidenceReachesTheStreamAndTheProjection(t *testing.T) {
	pool := idemPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_outbox_%d", now.UnixNano())

	conn, err := nats.Connect(streamURL(t))
	if err != nil {
		t.Skipf("no NATS at %s: %v", streamURL(t), err)
	}
	defer conn.Close()

	js, err := evidence.EnsureStream(ctx, conn)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	store := evidence.NewStore(pool)

	// Committed the way the pipeline commits: a batch, in one transaction, which is
	// also where the outbox row is written.
	event := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("outbox_%d", now.UnixNano()),
		EventName:     evidence.IntentReceived,
		TenantID:      tenant,
		AggregateID:   "env_outbox",
		CorrelationID: "corr_outbox",
		OccurredAt:    now,
		ProducedAt:    now,
		Producer:      "assurance-gateway",
		Sequence:      1,
	}
	if err := store.AppendBatch(ctx, []evidence.Event{event}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// The row exists because the decision committed, not because a publisher was
	// running: that is what makes publication safe rather than best effort.
	entries, err := store.Unpublished(ctx, 100)
	if err != nil {
		t.Fatalf("unpublished: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.EventID == event.EventID {
			found = true
		}
	}
	if !found {
		t.Fatal("a committed event owes nothing to the bus; the outbox row was not written")
	}

	consumer, err := evidence.NewConsumer(ctx, js, "test-outbox-"+event.EventID, "evidence.>")
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}

	publisher := &evidence.OutboxPublisher{
		Store:     store,
		Publisher: evidence.NewPublisher(js),
		Batch:     100,
		Report: func(published, failed int, err error) {
			t.Logf("drain: published=%d failed=%d err=%v", published, failed, err)
		},
	}
	if published := publisher.Drain(ctx); published == 0 {
		t.Fatal("the publisher drained nothing")
	}

	// Marked, so a restart does not republish everything.
	after, err := store.Unpublished(ctx, 100)
	if err != nil {
		t.Fatalf("unpublished after drain: %v", err)
	}
	for _, e := range after {
		if e.EventID == event.EventID {
			t.Error("the published event is still queued; a restart would send it again")
		}
	}

	// And the consumer end: the projection an analytical reader queries.
	base := os.Getenv("CLICKHOUSE_HTTP_URL")
	if base == "" {
		t.Skip("no CLICKHOUSE_HTTP_URL; the producer half is proved, the projection is not")
	}
	user := os.Getenv("CLICKHOUSE_USER")
	if user == "" {
		user = "assurance"
	}
	sink := fleet.NewSink(strings.TrimRight(base, "/"), user, os.Getenv("CLICKHOUSE_PASSWORD"))
	projection := &fleet.EvidenceProjection{Sink: sink}

	// Drained until this event appears rather than until the first batch arrives: a
	// durable consumer starting from the beginning of a stream will hand over other
	// runs' events first, and stopping at the first non-empty fetch would assert that
	// something was projected rather than that this was.
	query := fmt.Sprintf(
		"SELECT count() FROM assurance.evidence_stream WHERE tenant_id = '%s' AND event_id = '%s'",
		tenant, event.EventID)

	deadline := time.Now().Add(30 * time.Second)
	projected := 0
	for time.Now().Before(deadline) {
		count, err := consumer.Fetch(ctx, 100, 2*time.Second, projection.Handle)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		projected += count

		body, err := sink.Query(ctx, query)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		if strings.TrimSpace(body) != "0" {
			return
		}
		if count == 0 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	t.Errorf("the event never reached the projection; %d events were consumed", projected)
}
