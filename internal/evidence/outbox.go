package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The outbox: what makes publication safe rather than best-effort.
//
// EnsureStream, Publisher and Consumer have existed since Phase 6 with passing tests,
// and no binary ever constructed them. Evidence went straight to PostgreSQL, telemetry
// straight to ClickHouse, and the documentation called JetStream the event backbone —
// the project's own recurring defect, a component whose tests pass while the running
// producer never calls it.
//
// Publishing directly from the pipeline would have been the easy version and the wrong
// one: a broker that is briefly unreachable would silently drop the account of a
// decision that already happened. The row is written in the same transaction as the
// event, so a committed decision always has something owed to the bus, and a publisher
// that dies mid-flight resumes from the table.

// OutboxEntry is one event waiting to be published.
type OutboxEntry struct {
	OutboxID int64
	TenantID string
	EventID  string
	Subject  string
	Payload  []byte
	Attempts int
}

// Unpublished returns the oldest entries that have not reached the bus.
//
// Not tenant-scoped, and that is the one place in this repository where a read is not.
// The publisher drains every tenant's queue: scoping it per tenant would mean
// enumerating tenants from the credential registry, which drops a tenant the moment its
// credential rotates out, and a queue nobody drains is a queue that grows. It reads
// committed events on their way to a bus whose subjects carry the tenant, decides
// nothing, and returns nothing to a caller.
func (s *Store) Unpublished(ctx context.Context, limit int) ([]OutboxEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	rows, err := s.pool.Query(ctx, `
		SELECT outbox_id, tenant_id, event_id, subject, payload, attempt_count
		  FROM evidence_outbox
		 WHERE published_at IS NULL
		 ORDER BY created_at
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []OutboxEntry
	for rows.Next() {
		var entry OutboxEntry
		if err := rows.Scan(&entry.OutboxID, &entry.TenantID, &entry.EventID,
			&entry.Subject, &entry.Payload, &entry.Attempts); err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// MarkPublished records that an entry reached the bus.
//
// Inside the row's own tenant context. Reading the queue is deliberately not
// tenant-scoped, but writing is: the policy's WITH CHECK holds, and the first version
// of this silently failed every update because no tenant was set — the events were
// published and the queue never emptied.
func (s *Store) MarkPublished(ctx context.Context, tenantID string, outboxID int64,
	at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE evidence_outbox SET published_at = $2 WHERE outbox_id = $1`,
			outboxID, at.UTC())
		return err
	})
}

// MarkFailed records an attempt that did not.
//
// The row stays unpublished and is retried. Counting attempts is what lets an operator
// tell a bus that is down from a message that will never publish, and the last error is
// kept because "attempt 40" without a reason is a number nobody can act on.
func (s *Store) MarkFailed(ctx context.Context, tenantID string, outboxID int64,
	reason string) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE evidence_outbox
			   SET attempt_count = attempt_count + 1, last_error = $2
			 WHERE outbox_id = $1`, outboxID, reason)
		return err
	})
}

// enqueue writes outbox rows inside the caller's transaction.
//
// The same transaction as the events themselves, which is the whole point: a decision
// that committed always owes the bus a message, and a message never exists for a
// decision that did not commit.
func enqueue(ctx context.Context, tx pgx.Tx, events []Event) error {
	for _, e := range events {
		payload, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("event is not serialisable: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO evidence_outbox (tenant_id, event_id, subject, payload)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (event_id) DO NOTHING`,
			e.TenantID, e.EventID, e.Subject(), payload); err != nil {
			return err
		}
	}
	return nil
}

// OutboxPublisher drains the outbox onto the bus.
//
// It reports rather than logs. INV-013 keeps this package free of a logger — evidence
// is recorded, not logged, and the guard that caught this was right: a package that can
// write to a log is one where somebody eventually writes evidence to a log instead.
type OutboxPublisher struct {
	Store     *Store
	Publisher *Publisher
	Every     time.Duration
	Batch     int
	Now       func() time.Time

	// Report is called after each pass with what happened. The caller owns the log.
	Report func(published, failed int, err error)
}

func (o *OutboxPublisher) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// Run drains until the context ends.
//
// It never blocks a decision and never fails one: publication is downstream of a
// committed record, and a bus that is unreachable delays the analytical plane rather
// than the enforcement plane (INV-005).
func (o *OutboxPublisher) Run(ctx context.Context) {
	if o.Store == nil || o.Publisher == nil {
		return
	}
	if o.Every <= 0 {
		o.Every = time.Second
	}

	ticker := time.NewTicker(o.Every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.Drain(ctx)
		}
	}
}

// Drain publishes one batch and reports how many made it.
func (o *OutboxPublisher) Drain(ctx context.Context) int {
	entries, err := o.Store.Unpublished(ctx, o.Batch)
	if err != nil {
		o.report(0, 0, err)
		return 0
	}

	published, failed := 0, 0
	for _, entry := range entries {
		var event Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			// Unpublishable rather than transient. Retrying forever would hide it, so
			// it is counted and named and the queue moves on.
			_ = o.Store.MarkFailed(ctx, entry.TenantID, entry.OutboxID, "payload is not an event: "+err.Error())
			failed++
			continue
		}
		if err := o.Publisher.Publish(ctx, event); err != nil {
			_ = o.Store.MarkFailed(ctx, entry.TenantID, entry.OutboxID, err.Error())
			failed++
			continue
		}
		if err := o.Store.MarkPublished(ctx, entry.TenantID, entry.OutboxID, o.now()); err != nil {
			// Published and not marked: the consumer will see it twice. That is the
			// side to err on — at-least-once is the contract (ADR-008), and a
			// consumer that cannot tolerate a duplicate is the defect.
			failed++
			continue
		}
		published++
	}
	o.report(published, failed, nil)
	return published
}

func (o *OutboxPublisher) report(published, failed int, err error) {
	if o.Report != nil {
		o.Report(published, failed, err)
	}
}
