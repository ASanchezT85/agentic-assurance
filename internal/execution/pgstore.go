package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/broker"
)

// PostgresStore is the authoritative idempotency store (ADR-015).
//
// Every operation runs inside a tenant-scoped transaction, the same pattern the
// authority store uses, so row level security applies and app.tenant_id cannot leak
// to the next user of a pooled connection.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// ErrTenantContextMissing is returned when an operation is attempted without a
// tenant. Under row level security that would quietly match zero rows, which reads
// like "no such record" and would let a duplicate through as if it were new.
var ErrTenantContextMissing = errors.New("no tenant in context")

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
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

// Claim inserts a PENDING record, or returns the one already present.
//
// The atomicity is PostgreSQL's, not the application's: the primary key makes the
// insert either succeed or conflict, so two concurrent requests for one key cannot
// both believe they claimed it. Doing this with a read followed by a write would
// leave exactly the window INV-004 forbids.
// isEnvelopeReuse reports whether an insert failed on the one-envelope-one-submission
// index rather than on the idempotency key.
//
// Distinguished by the constraint name, because the two mean opposite things: a
// duplicate idempotency key is the normal replay this table exists to absorb, and a
// duplicate envelope id is a caller asking for a second order under one intent.
func isEnvelopeReuse(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "idempotency_envelope_idx"
}

func (s *PostgresStore) Claim(ctx context.Context, rec Record) (*Record, bool, error) {
	var (
		existing *Record
		claimed  bool
	)

	err := s.withTenant(ctx, rec.TenantID, func(tx pgx.Tx) error {
		// One statement on the path that matters.
		//
		// The tombstone check began as a separate SELECT before the insert, which put a
		// second round trip on every claim: measured at p50 4.94 ms against 3.34 ms
		// before, on the hot path's largest single item. It is a WHERE NOT EXISTS on the
		// insert instead, so the common case — a fresh key, nothing retired — costs what
		// it always did, and the extra reads happen only when nothing was inserted.
		tag, err := tx.Exec(ctx, `
			INSERT INTO idempotency_records
				(tenant_id, idempotency_key, envelope_id, client_order_id, state, created_at, updated_at)
			SELECT $1, $2, $3, $4, 'PENDING', $5, $5
			 WHERE NOT EXISTS (
			       SELECT 1 FROM idempotency_tombstones
			        WHERE tenant_id = $1
			          AND (idempotency_key = $2 OR envelope_id = $3))
			ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
			rec.TenantID, rec.IdempotencyKey, rec.EnvelopeID, rec.ClientOrderID, rec.CreatedAt.UTC())
		if isEnvelopeReuse(err) {
			// Named rather than surfaced as a constraint violation. An operator reading
			// "duplicate key" would look for a duplicate submission; the actual fault is
			// a caller that reused an envelope id under a new key.
			return ErrEnvelopeReused
		}
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 1 {
			claimed = true
			return nil
		}

		// Nothing was inserted. Either a live record holds the key, or the platform has
		// spent this identity and remembers it.
		loaded, err := loadInTx(ctx, tx, rec.TenantID, rec.IdempotencyKey)
		if err == nil {
			existing = loaded
			return nil
		}
		if !errors.Is(err, ErrRecordNotFound) {
			return err
		}
		return retiredInTx(ctx, tx, rec)
	})
	if err != nil {
		return nil, false, err
	}
	return existing, claimed, nil
}

// retiredInTx says which identity the platform has already spent.
//
// Reached only when the insert wrote nothing and no live record explains why, which leaves
// the tombstone. One query for both identities, because they are different refusals: a
// retired key is a request that was completed and whose outcome was pruned, and a retired
// envelope is a caller asking for a second order under an intent that already has one
// (§12.2).
func retiredInTx(ctx context.Context, tx pgx.Tx, rec Record) error {
	rows, err := tx.Query(ctx, `
		SELECT idempotency_key, envelope_id
		  FROM idempotency_tombstones
		 WHERE tenant_id = $1 AND (idempotency_key = $2 OR envelope_id = $3)`,
		rec.TenantID, rec.IdempotencyKey, rec.EnvelopeID)
	if err != nil {
		return err
	}
	defer rows.Close()

	keyRetired, envelopeRetired := false, false
	for rows.Next() {
		var k, e string
		if err := rows.Scan(&k, &e); err != nil {
			return err
		}
		if k == rec.IdempotencyKey {
			keyRetired = true
		}
		if rec.EnvelopeID != "" && e == rec.EnvelopeID && k != rec.IdempotencyKey {
			envelopeRetired = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	switch {
	case keyRetired:
		return ErrKeyRetired
	case envelopeRetired:
		return ErrEnvelopeReused
	}
	// Nothing inserted, no live record, no tombstone. The row was taken and released
	// between the two statements, which is a retry racing itself: the caller is told to
	// try again rather than being handed somebody else's outcome.
	return fmt.Errorf("the idempotency record for %s could not be claimed or read",
		rec.IdempotencyKey)
}

// Resolve writes the final outcome.
//
// It refuses to overwrite an outcome that is already recorded. A resolved record is
// the deterministic answer for its key, and rewriting it would make a duplicate
// return something different from what the first caller was told.
func (s *PostgresStore) Resolve(ctx context.Context, tenantID, idempotencyKey string, o Outcome, at time.Time) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE idempotency_records
			   SET state = 'RESOLVED',
			       outcome_state = $3,
			       broker_order_id = $4,
			       filled_quantity = $5,
			       reject_reason = $6,
			       updated_at = $7
			 WHERE tenant_id = $1
			   AND idempotency_key = $2
			   AND state = 'PENDING'`,
			tenantID, idempotencyKey,
			string(o.State), nullIfEmpty(o.BrokerOrderID), o.FilledQuantity,
			nullIfEmpty(o.RejectReason), at.UTC())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Either the record is gone or it was already resolved. Both mean this
			// caller must not claim to have decided the outcome.
			return ErrRecordNotFound
		}
		return nil
	})
}

// LoadByEnvelope returns the record a submitted intent produced.
//
// Separate from Load because a caller knows the envelope it sent, not the idempotency
// key the platform derived a client order id from. Both reach the same row.
func (s *PostgresStore) LoadByEnvelope(ctx context.Context, tenantID, envelopeID string) (*Record, error) {
	var rec *Record
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var key string
		err := tx.QueryRow(ctx,
			`SELECT idempotency_key FROM idempotency_records
			 WHERE tenant_id = $1 AND envelope_id = $2`, tenantID, envelopeID).Scan(&key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		rec, err = loadInTx(ctx, tx, tenantID, key)
		return err
	})
	return rec, err
}

func (s *PostgresStore) Load(ctx context.Context, tenantID, idempotencyKey string) (*Record, error) {
	var rec *Record
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		loaded, err := loadInTx(ctx, tx, tenantID, idempotencyKey)
		if err != nil {
			return err
		}
		rec = loaded
		return nil
	})
	return rec, err
}

func loadInTx(ctx context.Context, tx pgx.Tx, tenantID, idempotencyKey string) (*Record, error) {
	var (
		rec          Record
		state        string
		outcomeState *string
		brokerID     *string
		filledQty    *float64
		rejectReason *string
	)

	err := tx.QueryRow(ctx, `
		SELECT tenant_id, idempotency_key, envelope_id, client_order_id, state,
		       outcome_state, broker_order_id, filled_quantity, reject_reason,
		       created_at, updated_at
		  FROM idempotency_records
		 WHERE tenant_id = $1 AND idempotency_key = $2`,
		tenantID, idempotencyKey).
		Scan(&rec.TenantID, &rec.IdempotencyKey, &rec.EnvelopeID, &rec.ClientOrderID, &state,
			&outcomeState, &brokerID, &filledQty, &rejectReason,
			&rec.CreatedAt, &rec.UpdatedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRecordNotFound
	}
	if err != nil {
		return nil, err
	}

	rec.State = RecordState(state)
	rec.Outcome = Outcome{ClientOrderID: rec.ClientOrderID}
	if outcomeState != nil {
		rec.Outcome.State = broker.ExecutionState(*outcomeState)
	}
	if brokerID != nil {
		rec.Outcome.BrokerOrderID = *brokerID
	}
	if filledQty != nil {
		rec.Outcome.FilledQuantity = *filledQty
	}
	if rejectReason != nil {
		rec.Outcome.RejectReason = *rejectReason
	}
	return &rec, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
