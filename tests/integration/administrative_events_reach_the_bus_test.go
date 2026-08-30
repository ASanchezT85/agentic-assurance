//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
)

// A-5-01: every event the platform records is owed to the bus, not only the batched ones.
//
// AppendBatch enqueued to the outbox and Append did not. The hot path batches, so its six
// events per decision reached the analytical plane; everything written one at a time did
// not. That is the administrative half of the record — a grant issued or revoked, a control
// applied or lifted, a signing key registered, an incident opened, a simulation run — and it
// is exactly the half an incident review goes looking for.
//
// Found on the running gateway: issuing a grant over HTTP wrote authority.grant.issued.v1
// into PostgreSQL with no outbox row beside it.
func TestEveryRecordedEventIsQueuedForTheBus(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)
	store := evidence.NewStore(pool)
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_a5_%d", now.UnixNano())

	event := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("a5_%d", now.UnixNano()),
		EventName:     evidence.AuthorityIssued,
		TenantID:      tenant,
		AggregateID:   "grant_a5",
		CorrelationID: "grant_a5",
		OccurredAt:    now,
		ProducedAt:    now,
		Producer:      "assurance-gateway",
		Sequence:      1,
		Payload:       map[string]any{"issued_by": "audit"},
	}
	if _, err := store.Append(ctx, event); err != nil {
		t.Fatalf("append: %v", err)
	}

	queued := queuedEventIDs(t, tenant)
	if !queued[event.EventID] {
		t.Fatalf("%s was recorded and never queued. An event in PostgreSQL and not in "+
			"the outbox never reaches NATS or the analytical store: the enforcement "+
			"plane's own record is complete and the copy an incident review reads is "+
			"missing every act an operator performed.", event.EventID)
	}

	// Appending it again is not a second delivery.
	if _, err := store.Append(ctx, event); err != nil {
		t.Fatalf("second append: %v", err)
	}
	if n := queuedCount(t, tenant); n != 1 {
		t.Errorf("%d outbox rows for one event appended twice", n)
	}
}

// And an event that arrived from the bus is not queued back onto it.
func TestAConsumedEventIsNotQueuedBack(t *testing.T) {
	ctx := context.Background()
	store := evidence.NewStore(idemPool(t))
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_a5c_%d", now.UnixNano())

	event := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("a5c_%d", now.UnixNano()),
		EventName:     evidence.AuthorityEvaluated,
		TenantID:      tenant,
		AggregateID:   "env_a5c",
		CorrelationID: "corr_a5c",
		OccurredAt:    now,
		ProducedAt:    now,
		Producer:      "assurance-gateway",
		Sequence:      1,
	}
	if _, err := store.AppendConsumed(ctx, event); err != nil {
		t.Fatalf("append consumed: %v", err)
	}

	if n := queuedCount(t, tenant); n != 0 {
		t.Errorf("an event that came from the bus was queued back onto it (%d rows); a "+
			"consumer that republishes what it consumes is a loop", n)
	}
}

func queuedEventIDs(t *testing.T, tenant string) map[string]bool {
	t.Helper()
	ctx := context.Background()
	pool := outboxPool(t)

	rows, err := pool.Query(ctx,
		`SELECT event_id FROM evidence_outbox WHERE tenant_id = $1`, tenant)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()

	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids[id] = true
	}
	return ids
}

func queuedCount(t *testing.T, tenant string) int {
	t.Helper()
	return len(queuedEventIDs(t, tenant))
}
