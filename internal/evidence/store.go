package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTenantContextMissing is returned when a query is attempted without a tenant.
var ErrTenantContextMissing = errors.New("no tenant in context")

// Store is the append-only evidence repository.
//
// It exposes Append, Chain and ByAggregate. There is no Update and no Delete, and
// the database would refuse them anyway: the application role holds only SELECT and
// INSERT, and a trigger rejects the rest (migration 0003).
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) withTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return ErrTenantContextMissing
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return fmt.Errorf("set tenant: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Append records an event.
//
// It is idempotent by event id. At-least-once delivery is the design assumption
// (ADR-008), so a redelivered event must be a no-op rather than a second row: the
// alternative is a timeline that double-counts every event the network hiccuped on.
//
// The returned bool reports whether this call was the one that recorded it.
func (s *Store) Append(ctx context.Context, e Event) (recorded bool, err error) {
	if err := e.Validate(); err != nil {
		return false, err
	}

	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return false, fmt.Errorf("payload is not serialisable: %w", err)
	}
	if e.Payload == nil {
		payload = []byte(`{}`)
	}

	err = s.withTenant(ctx, e.TenantID, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO evidence_events
				(event_id, schema_version, event_name, tenant_id, aggregate_id,
				 correlation_id, causation_id, occurred_at, produced_at, producer,
				 sequence, corrects_event_id, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			-- The partition key is part of the key now that evidence is partitioned by
			-- month. A redelivered event carries the moment it happened, so this
			-- deduplicates exactly as (event_id) did (ADR-008).
			ON CONFLICT (event_id, occurred_at) DO NOTHING`,
			e.EventID, e.SchemaVersion, string(e.EventName), e.TenantID, e.AggregateID,
			e.CorrelationID, nullIfEmpty(e.CausationID), e.OccurredAt.UTC(), e.ProducedAt.UTC(),
			e.Producer, e.Sequence, nullIfEmpty(e.CorrectsEventID), payload)
		if execErr != nil {
			return execErr
		}
		recorded = tag.RowsAffected() == 1
		if !recorded {
			// Already recorded. Queueing it again would be a second delivery of an event
			// the bus has already been told about.
			return nil
		}
		// And the bus is owed a message, in the same transaction.
		//
		// It was not, and only AppendBatch enqueued, so every event written one at a
		// time — a grant issued or revoked, a control applied or lifted, a key
		// registered, an incident opened, a simulation run — reached PostgreSQL and
		// never reached the analytical plane. The hot path was covered because it
		// batches; the administrative acts, which are exactly what an incident review
		// looks for, were invisible in ClickHouse.
		return enqueue(ctx, tx, []Event{e})
	})
	return recorded, err
}

// AppendConsumed records an event that arrived from the bus.
//
// The same write without the outbox row: this event has already been delivered, and
// queueing it again would publish it back to the stream it came from, for ever.
func (s *Store) AppendConsumed(ctx context.Context, e Event) (bool, error) {
	if err := e.Validate(); err != nil {
		return false, err
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return false, fmt.Errorf("payload is not serialisable: %w", err)
	}
	if e.Payload == nil {
		payload = []byte(`{}`)
	}

	recorded := false
	err = s.withTenant(ctx, e.TenantID, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO evidence_events
				(event_id, schema_version, event_name, tenant_id, aggregate_id,
				 correlation_id, causation_id, occurred_at, produced_at, producer,
				 sequence, corrects_event_id, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (event_id, occurred_at) DO NOTHING`,
			e.EventID, e.SchemaVersion, string(e.EventName), e.TenantID, e.AggregateID,
			e.CorrelationID, nullIfEmpty(e.CausationID), e.OccurredAt.UTC(), e.ProducedAt.UTC(),
			e.Producer, e.Sequence, nullIfEmpty(e.CorrectsEventID), payload)
		if execErr != nil {
			return execErr
		}
		recorded = tag.RowsAffected() == 1
		return nil
	})
	return recorded, err
}

// AppendBatch records several events in one transaction.
//
// One round trip instead of one per event, and that is the whole point. A submission
// produces six events, and written one at a time they were six transactions — six
// times the cost of the decision they describe, measured at about 95 ms of the 120 ms
// an accepted intent took while the enforcement computation itself is 12.5 us.
//
// All events must belong to one tenant: the transaction sets app.tenant_id once, and a
// batch that mixed tenants would write some of them under the wrong one.
func (s *Store) AppendBatch(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	tenantID := events[0].TenantID
	rows := make([][]any, 0, len(events))
	for _, e := range events {
		if e.TenantID != tenantID {
			return fmt.Errorf("evidence batch mixes tenants %q and %q", tenantID, e.TenantID)
		}
		if err := e.Validate(); err != nil {
			return err
		}
		payload, err := json.Marshal(e.Payload)
		if err != nil {
			return fmt.Errorf("payload is not serialisable: %w", err)
		}
		if e.Payload == nil {
			payload = []byte(`{}`)
		}
		rows = append(rows, []any{
			e.EventID, e.SchemaVersion, string(e.EventName), e.TenantID, e.AggregateID,
			e.CorrelationID, nullIfEmpty(e.CausationID), e.OccurredAt.UTC(), e.ProducedAt.UTC(),
			e.Producer, e.Sequence, nullIfEmpty(e.CorrectsEventID), payload,
		})
	}

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		batch := &pgx.Batch{}
		for _, row := range rows {
			batch.Queue(`
				INSERT INTO evidence_events
					(event_id, schema_version, event_name, tenant_id, aggregate_id,
					 correlation_id, causation_id, occurred_at, produced_at, producer,
					 sequence, corrects_event_id, payload)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
				-- The partition key is part of the key now that evidence is partitioned by
			-- month. A redelivered event carries the moment it happened, so this
			-- deduplicates exactly as (event_id) did (ADR-008).
			ON CONFLICT (event_id, occurred_at) DO NOTHING`, row...)
		}
		results := tx.SendBatch(ctx, batch)
		for range rows {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return err
			}
		}
		if err := results.Close(); err != nil {
			return err
		}

		// The outbox, in the same transaction. A committed decision always owes the
		// bus a message, and a message never exists for a decision that did not
		// commit — which is what publishing from the pipeline could not promise.
		return enqueue(ctx, tx, events)
	})
}

// Chain returns every event for a correlation id, in the order it happened.
//
// This is the query ADR-023 exists for and the one spec section 66 step 19 walks:
// agent, intent, authority, policy, broker order, result. Corrections appear as
// later events referencing earlier ones; nothing is collapsed or merged, because a
// reader needs to see that a correction happened, not a tidied result.
func (s *Store) Chain(ctx context.Context, tenantID, correlationID string) ([]Event, error) {
	return s.query(ctx, tenantID, `
		SELECT event_id, schema_version, event_name, tenant_id, aggregate_id,
		       correlation_id, causation_id, occurred_at, produced_at, producer,
		       sequence, corrects_event_id, payload
		  FROM evidence_events
		 WHERE tenant_id = $1 AND correlation_id = $2
		 ORDER BY occurred_at ASC, sequence ASC, event_id ASC`,
		tenantID, correlationID)
}

// ByAggregate returns every event about one object.
func (s *Store) ByAggregate(ctx context.Context, tenantID, aggregateID string) ([]Event, error) {
	return s.query(ctx, tenantID, `
		SELECT event_id, schema_version, event_name, tenant_id, aggregate_id,
		       correlation_id, causation_id, occurred_at, produced_at, producer,
		       sequence, corrects_event_id, payload
		  FROM evidence_events
		 WHERE tenant_id = $1 AND aggregate_id = $2
		 ORDER BY occurred_at ASC, sequence ASC, event_id ASC`,
		tenantID, aggregateID)
}

// ByPeriod returns every event a tenant produced in a half-open window, in the order
// an archive of it has to be read back.
//
// Deterministic ordering is the point rather than a convenience: the archive's hash
// chain is computed over this sequence, and a query that returned the same rows in a
// different order would produce a different chain head and make a faithful archive look
// tampered with.
func (s *Store) ByPeriod(ctx context.Context, tenantID string, from, to time.Time) ([]Event, error) {
	return s.query(ctx, tenantID, `
		SELECT event_id, schema_version, event_name, tenant_id, aggregate_id,
		       correlation_id, causation_id, occurred_at, produced_at, producer,
		       sequence, corrects_event_id, payload
		  FROM evidence_events
		 WHERE tenant_id = $1 AND occurred_at >= $2 AND occurred_at < $3
		 ORDER BY occurred_at ASC, sequence ASC, event_id ASC`,
		tenantID, from.UTC(), to.UTC())
}

// RecentAggregates returns every event of the most recently active aggregates for a
// tenant, newest aggregate first and each one's events in order.
//
// Two steps rather than one, and the reason is what the endpoint above it means: the
// inner query picks which envelopes to show by when they were last touched, and the
// outer one returns those envelopes whole. Selecting events by time and grouping
// afterwards would return a page of fragments — an authority decision here, a broker
// result there — and a caller could not tell a refusal from a half-read chain.
func (s *Store) RecentAggregates(ctx context.Context, tenantID string, since time.Time,
	limit int) ([]Event, error) {

	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.query(ctx, tenantID, `
		WITH envelopes AS (
		    -- The newest intents, straight off the index, rather than a summary of the
		    -- window. Aggregates that are intents: everything writes evidence, and a
		    -- list of "intents" holding a revoked control would be a list of aggregates
		    -- wearing the wrong name.
		    --
		    -- Ordered by when the intent arrived rather than by its last event. The
		    -- earlier version ranked by last activity, which meant grouping every event
		    -- of every envelope in the window — 909,061 rows into 177,087 groups to
		    -- return fifty, about half a second on a day of real traffic.
		    SELECT aggregate_id, occurred_at AS received_at
		      FROM evidence_events
		     WHERE tenant_id = $1 AND event_name = $4 AND occurred_at >= $2
		     ORDER BY occurred_at DESC
		     LIMIT $3
		)
		SELECT e.event_id, e.schema_version, e.event_name, e.tenant_id, e.aggregate_id,
		       e.correlation_id, e.causation_id, e.occurred_at, e.produced_at, e.producer,
		       e.sequence, e.corrects_event_id, e.payload
		  FROM evidence_events e
		  JOIN envelopes v ON v.aggregate_id = e.aggregate_id
		 WHERE e.tenant_id = $1
		 ORDER BY v.received_at DESC, e.occurred_at ASC, e.sequence ASC, e.event_id ASC`,
		tenantID, since.UTC(), limit, string(IntentReceived))
}

func (s *Store) query(ctx context.Context, tenantID, sql string, args ...any) ([]Event, error) {
	var out []Event
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var (
				e         Event
				name      string
				causation *string
				corrects  *string
				payload   []byte
			)
			if err := rows.Scan(&e.EventID, &e.SchemaVersion, &name, &e.TenantID,
				&e.AggregateID, &e.CorrelationID, &causation, &e.OccurredAt, &e.ProducedAt,
				&e.Producer, &e.Sequence, &corrects, &payload); err != nil {
				return err
			}
			e.EventName = EventName(name)
			if causation != nil {
				e.CausationID = *causation
			}
			if corrects != nil {
				e.CorrectsEventID = *corrects
			}
			if len(payload) > 0 {
				_ = json.Unmarshal(payload, &e.Payload)
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Correct records a correction to an earlier event.
//
// It is the only way to say "that was wrong", and it works by adding a row. The
// earlier event stays exactly as recorded, which is what makes a timeline an
// account of what was believed at the time rather than a summary of what is
// believed now (ADR-009).
func (s *Store) Correct(ctx context.Context, correction Event, correctsEventID string) error {
	if correctsEventID == "" {
		return &ValidationError{"corrects_event_id", "a correction must name the event it supersedes"}
	}
	correction.EventName = EvidenceCorrected
	correction.CorrectsEventID = correctsEventID

	// The superseded event has to exist, or the correction points at nothing.
	prior, err := s.byID(ctx, correction.TenantID, correctsEventID)
	if err != nil {
		return fmt.Errorf("cannot correct %s: %w", correctsEventID, err)
	}
	if correction.CorrelationID == "" {
		correction.CorrelationID = prior.CorrelationID
	}
	if correction.AggregateID == "" {
		correction.AggregateID = prior.AggregateID
	}

	_, err = s.Append(ctx, correction)
	return err
}

func (s *Store) byID(ctx context.Context, tenantID, eventID string) (*Event, error) {
	events, err := s.query(ctx, tenantID, `
		SELECT event_id, schema_version, event_name, tenant_id, aggregate_id,
		       correlation_id, causation_id, occurred_at, produced_at, producer,
		       sequence, corrects_event_id, payload
		  FROM evidence_events
		 WHERE tenant_id = $1 AND event_id = $2`, tenantID, eventID)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrNotFound
	}
	return &events[0], nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// RecordedAt is not on Event on purpose.
//
// occurred_at is when the thing happened, produced_at is when the producer said so,
// and recorded_at is when this database saw it. The third is the database's own
// observation and is not something a producer may assert, so it is set by a column
// default and never read back into a producer-supplied structure.
var _ = time.Time{}

// AppendInTx records one event and its outbox row inside a caller's transaction.
//
// It exists so an act that changes what the platform will accept — registering a signing
// key, granting policy authority — can commit with its evidence or not at all. Written
// here rather than copied into each caller because the two serialisations below are easy
// to get subtly wrong: evidence_events stores the payload column, and the outbox carries
// the whole event, because the publisher unmarshals a row back into an Event. They were
// once the same value, and every affected event failed validation on the way out and
// stayed in the queue for ever.
//
// The caller is responsible for having set app.tenant_id on the transaction.
func AppendInTx(ctx context.Context, tx pgx.Tx, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	queued, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_events
			(event_id, schema_version, event_name, tenant_id, aggregate_id,
			 correlation_id, causation_id, occurred_at, produced_at, producer,
			 sequence, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (event_id, occurred_at) DO NOTHING`,
		e.EventID, e.SchemaVersion, string(e.EventName), e.TenantID, e.AggregateID,
		e.CorrelationID, nullIfEmpty(e.CausationID), e.OccurredAt.UTC(), e.ProducedAt.UTC(),
		e.Producer, e.Sequence, payload); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO evidence_outbox (tenant_id, event_id, subject, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id) DO NOTHING`,
		e.TenantID, e.EventID, e.Subject(), queued)
	return err
}
