package execution

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Bounded retention for idempotency records (spec section 19).
//
// The section lists it beside a unique envelope id and deterministic duplicate
// handling, and nothing implemented it: the table grew with every intent the platform
// ever decided. It is control state rather than the account of what happened — the
// evidence chain is that, and it is append-only and untouched by any of this.
//
// Two rules shape what may be deleted.
//
// A PENDING record is never pruned, at any age. It says a submission was claimed and
// the platform does not know what the venue did, which is the one thing an operator
// has to reconcile; deleting it would turn an unresolved order into an order nobody
// remembers claiming (spec section 19, ADR-015).
//
// And pruning a resolved record does not reopen its key (ADR-027). What is deleted is the
// cached outcome and the ability to replay it; what the key means is held elsewhere and
// permanently: the key and the envelope move into idempotency_tombstones in the same
// transaction that deletes the record, so a key presented again — at any age, under any
// envelope — is refused as retired.
//
// This file used to say the opposite: that pruning reopened the key and a later caller got
// a fresh execution. It was then corrected to say authority_usage remembered instead, and
// that was true only for requests authority could price. An identical request re-presented
// after the prune read as a retry that may proceed, and reached the venue a second time
// (F4-B001); a quantity-sized market order has no reservation row at all. The window
// bounds storage. It does not bound what the platform remembers about a key.

// DefaultRetention is how long a resolved record is kept.
//
// Thirty days rather than a day: retries happen in minutes, but reconciliation, disputes
// and "what did this agent do last month" happen over weeks, and this is how long a
// duplicate can still be answered with the outcome it originally got. Past it a repeated
// key is refused rather than replayed — refused rather than re-executed, which is the
// part that matters (ADR-027).
const DefaultRetention = 30 * 24 * time.Hour

// PruneBatch bounds one delete so a sweep never holds a long lock on the hot path's
// table. The sweeper repeats until a pass deletes less than this.
const PruneBatch = 5000

// Prune deletes resolved records older than a cutoff, for one tenant, in one batch.
//
// Per tenant because row level security is per tenant and this store has no method
// that reads without one: a sweeper that could see every tenant's rows would be the
// one component allowed to ignore INV-007.
func (s *PostgresStore) Prune(ctx context.Context, tenantID string, before time.Time) (int64, error) {
	var deleted int64
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		// The identity moves to the tombstone in the same transaction that deletes the
		// record. There is no instant at which the outcome is gone and the key is
		// forgotten: a caller arriving between the two would be a fresh request under a
		// key that had already reached a venue.
		//
		// The batch is chosen once and both statements operate on exactly that set, so
		// nothing is deleted whose identity was not first retired.
		rows, err := tx.Query(ctx, `
			WITH doomed AS (
			    SELECT tenant_id, idempotency_key, envelope_id, client_order_id,
			           COALESCE(outcome_state, 'UNKNOWN') AS final_state
			      FROM idempotency_records
			     WHERE state = $1 AND updated_at < $2
			     ORDER BY updated_at
			     LIMIT $3
			     FOR UPDATE
			), retired AS (
			    INSERT INTO idempotency_tombstones
			        (tenant_id, idempotency_key, envelope_id, client_order_id,
			         final_state, retired_at)
			    SELECT tenant_id, idempotency_key, envelope_id, client_order_id,
			           final_state, $4
			      FROM doomed
			    ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
			)
			SELECT idempotency_key FROM doomed`,
			string(RecordResolved), before.UTC(), PruneBatch, time.Now().UTC())
		if err != nil {
			return err
		}
		var keys []string
		for rows.Next() {
			var k string
			if err := rows.Scan(&k); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(keys) == 0 {
			return nil
		}

		tag, err := tx.Exec(ctx, `
			DELETE FROM idempotency_records
			 WHERE tenant_id = $1 AND idempotency_key = ANY($2)`,
			tenantID, keys)
		if err != nil {
			return err
		}
		deleted = tag.RowsAffected()
		return nil
	})
	return deleted, err
}

// Sweeper prunes on a schedule.
//
// It runs in the gateway rather than as a separate job because the gateway is the
// process that owns this table, and a retention job deployed separately is one that
// can be forgotten in an environment and quietly stop bounding anything.
type Sweeper struct {
	Store  *PostgresStore
	Every  time.Duration
	Keep   time.Duration
	Log    *slog.Logger
	Now    func() time.Time
	Tenant func() []string
}

func (s *Sweeper) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sweeper) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Run sweeps until the context ends. It never fails anything: retention is
// housekeeping, and a database that refuses a delete must not stop the platform from
// deciding (spec section 17).
func (s *Sweeper) Run(ctx context.Context) {
	if s.Store == nil || s.Tenant == nil {
		return
	}
	if s.Every <= 0 {
		s.Every = time.Hour
	}
	if s.Keep <= 0 {
		s.Keep = DefaultRetention
	}

	ticker := time.NewTicker(s.Every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep runs one pass over every tenant this process serves.
//
// A tenant removed from the credential registry stops being swept, which is worth
// knowing: retention follows who the gateway serves, not who has rows.
func (s *Sweeper) Sweep(ctx context.Context) int64 {
	before := s.now().Add(-s.Keep)

	var total int64
	for _, tenant := range s.Tenant() {
		for {
			deleted, err := s.Store.Prune(ctx, tenant, before)
			if err != nil {
				s.log().Warn("idempotency retention pass failed",
					"tenant", tenant, "err", err,
					"consequence", "resolved records older than the window are still stored")
				break
			}
			total += deleted
			if deleted < PruneBatch {
				break
			}
		}
	}

	if total > 0 {
		s.log().Info("idempotency records pruned",
			"deleted", total,
			"older_than", before.UTC().Format(time.RFC3339),
			"note", fmt.Sprintf("pending records are never pruned; the evidence chain is untouched"))
	}
	return total
}
