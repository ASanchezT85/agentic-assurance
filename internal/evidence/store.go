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
			ON CONFLICT (event_id) DO NOTHING`,
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
