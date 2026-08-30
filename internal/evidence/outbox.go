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
	return s.read(ctx, limit, `
		SELECT outbox_id, tenant_id, event_id, subject, payload, attempt_count
		  FROM evidence_outbox
		 WHERE published_at IS NULL
		 ORDER BY created_at
		 LIMIT $1`)
}

// Claim takes a batch of unpublished rows and leases them.
//
// FOR UPDATE SKIP LOCKED, so two publishers draining the same queue take different work
// instead of the same work twice. Under a backlog that difference is the whole point:
// duplicate publication is harmless for correctness — delivery is at-least-once and the
// consumer deduplicates by event id — and it consumes exactly the capacity the backlog
// needs to clear.
//
// The lease expires. A publisher that dies mid-batch must not strand its rows, so a claim
// older than reclaimAfter is available again. Re-publishing what it may have already sent
// is the safe side of that trade.
func (s *Store) Claim(ctx context.Context, limit int, owner string, at time.Time,
	reclaimAfter time.Duration) ([]OutboxEntry, error) {

	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	return s.read(ctx, limit, `
		UPDATE evidence_outbox
		   SET claimed_at = $2, claimed_by = $3
		 WHERE outbox_id IN (
			 SELECT outbox_id
			   FROM evidence_outbox
			  WHERE published_at IS NULL
			    AND (claimed_at IS NULL OR claimed_at < $4)
			  ORDER BY created_at
			  LIMIT $1
			  FOR UPDATE SKIP LOCKED
		 )
		 RETURNING outbox_id, tenant_id, event_id, subject, payload, attempt_count`,
		at.UTC(), owner, at.Add(-reclaimAfter).UTC())
}

func (s *Store) read(ctx context.Context, limit int, sql string, args ...any) ([]OutboxEntry, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, sql, append([]any{limit}, args...)...)
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

// Depth is what an operator needs to see: how much is queued and how old the oldest is.
//
// Queue depth alone says nothing — a thousand rows a second old is healthy and ten rows
// an hour old is a stall. Both numbers together are the signal.
// An empty tenant means the whole queue, which is what an operator watches; a named one
// is what a measurement of a single workload needs, because a shared environment always
// has somebody else's rows in it.
func (s *Store) Depth(ctx context.Context, tenantID string) (queued int64, oldest time.Time, err error) {
	var oldestAt *time.Time
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), min(created_at)
		  FROM evidence_outbox
		 WHERE published_at IS NULL
		   AND ($1 = '' OR tenant_id = $1)`, tenantID).Scan(&queued, &oldestAt)
	if err != nil {
		return 0, time.Time{}, err
	}
	if oldestAt != nil {
		oldest = *oldestAt
	}
	return queued, oldest, nil
}

// MarkPublishedBatch marks a whole drained batch in one statement.
//
// One round trip for a batch rather than one transaction per event. Marking was two
// round trips per event — publish, then a transaction to record it — which put the
// service rate an order of magnitude below the arrival rate all by itself.
//
// The publisher role's policy permits this across tenants, which is what a queue drained
// by one process requires; the application role still sees only its own tenant.
func (s *Store) MarkPublishedBatch(ctx context.Context, ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE evidence_outbox SET published_at = $2 WHERE outbox_id = ANY($1)`,
		ids, at.UTC())
	return err
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

	// Owner identifies this publisher in a claimed row, so an operator reading a
	// stalled queue can see which instance holds it.
	Owner string

	// ReclaimAfter is how long a claim survives its publisher. A process that dies
	// mid-batch must not strand its rows; two minutes by default.
	ReclaimAfter time.Duration

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
	if o.Batch <= 0 {
		o.Batch = 500
	}

	for {
		// Drain while there is a backlog, and wait only when there is not.
		//
		// It used to publish one batch per tick, so the service rate was a constant —
		// batch divided by interval — regardless of how much had arrived. A queue whose
		// service rate does not respond to its depth diverges the moment arrivals
		// exceed it, and this one did: 2,200 events per second in and 100 to 200 out.
		published := o.Drain(ctx)
		if published > 0 {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(o.Every):
		}
	}
}

// Drain claims one batch, publishes it, and marks what made it.
func (o *OutboxPublisher) Drain(ctx context.Context) int {
	batch := o.Batch
	if batch <= 0 {
		batch = 500
	}
	entries, err := o.Store.Claim(ctx, batch, o.owner(), o.now(), o.reclaimAfter())
	if err != nil {
		o.report(0, 0, err)
		return 0
	}

	published := make([]int64, 0, len(entries))
	failed := 0

	// Decode first, so an unpublishable row is separated from a publishing failure.
	events := make([]Event, 0, len(entries))
	ids := make([]int64, 0, len(entries))
	tenants := make([]string, 0, len(entries))
	for _, entry := range entries {
		var event Event
		if err := json.Unmarshal(entry.Payload, &event); err != nil {
			// Unpublishable rather than transient. Retrying forever would hide it, so
			// it is counted and named and the queue moves on.
			_ = o.Store.MarkFailed(ctx, entry.TenantID, entry.OutboxID,
				"payload is not an event: "+err.Error())
			failed++
			continue
		}
		events = append(events, event)
		ids = append(ids, entry.OutboxID)
		tenants = append(tenants, entry.TenantID)
	}

	if len(events) > 0 {
		// One batch in flight rather than one message at a time. The publisher waits for
		// every acknowledgement before anything is marked published; what changed is
		// that it no longer waits for each one before sending the next, which was the
		// outbox's throughput ceiling.
		results, err := o.Publisher.PublishBatch(ctx, events)
		if err != nil {
			o.report(0, len(events)+failed, err)
			return 0
		}
		for i := range events {
			if results[i] != nil {
				_ = o.Store.MarkFailed(ctx, tenants[i], ids[i], results[i].Error())
				failed++
				continue
			}
			published = append(published, ids[i])
		}
	}

	if err := o.Store.MarkPublishedBatch(ctx, published, o.now()); err != nil {
		// Published and not marked: the consumer will see them again. That is the side
		// to err on — at-least-once is the contract (ADR-008), and a consumer that
		// cannot tolerate a duplicate is the defect.
		o.report(0, len(published)+failed, err)
		return 0
	}

	o.report(len(published), failed, nil)
	return len(published)
}

func (o *OutboxPublisher) owner() string {
	if o.Owner != "" {
		return o.Owner
	}
	return "assurance-gateway"
}

func (o *OutboxPublisher) reclaimAfter() time.Duration {
	if o.ReclaimAfter > 0 {
		return o.ReclaimAfter
	}
	return 2 * time.Minute
}

func (o *OutboxPublisher) report(published, failed int, err error) {
	if o.Report != nil {
		o.Report(published, failed, err)
	}
}
