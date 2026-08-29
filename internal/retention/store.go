package retention

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is where holds and manifests live.
//
// Migration 0021 created these tables and nothing in Go ever read or wrote them, so the
// retention rules were exercised against fixtures and the schema was exercised by
// nobody. Every method here goes through withTenant, because a hold or a manifest is a
// statement about one customer's evidence and RLS is what keeps it that way.
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// ErrTenantContextMissing is returned rather than running a query with no tenant, where
// RLS would answer with an empty result and an empty result reads like "no holds".
var ErrTenantContextMissing = errors.New("no tenant in context")

// ErrNoManifest is a partition that was never exported.
var ErrNoManifest = errors.New("no manifest for this partition")

func (s *PostgresStore) withTenant(ctx context.Context, tenantID string,
	fn func(pgx.Tx) error) error {

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

// SaveManifest records what an export produced.
//
// Re-exporting the same partition overwrites it, because a second export of a period is
// a new archive of the same evidence rather than a second archive: the chain head is
// derived from the events, so an honest re-export writes the same head and a different
// one means the source changed and an operator needs to see the current truth.
func (s *PostgresStore) SaveManifest(ctx context.Context, m Manifest) error {
	return s.withTenant(ctx, m.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO archive_manifests
				(tenant_id, manifest_id, partition, period_start, period_end,
				 event_count, chain_head, destination, exported_at, exported_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (tenant_id, manifest_id) DO UPDATE SET
				partition   = EXCLUDED.partition,
				period_start = EXCLUDED.period_start,
				period_end   = EXCLUDED.period_end,
				event_count  = EXCLUDED.event_count,
				chain_head   = EXCLUDED.chain_head,
				destination  = EXCLUDED.destination,
				exported_at  = EXCLUDED.exported_at,
				exported_by  = EXCLUDED.exported_by,
				-- A re-export is unverified until somebody verifies the new archive.
				verified_at  = NULL,
				verified_by  = NULL`,
			m.TenantID, m.ManifestID, m.Partition, m.PeriodStart.UTC(), m.PeriodEnd.UTC(),
			m.EventCount, m.ChainHead, m.Destination, m.ExportedAt.UTC(), m.ExportedBy)
		return err
	})
}

// Manifest returns what was exported for a partition.
func (s *PostgresStore) Manifest(ctx context.Context, tenantID, partition string) (*Manifest, error) {
	var m Manifest
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT tenant_id, manifest_id, partition, period_start, period_end,
			       event_count, chain_head, destination, exported_at, exported_by,
			       verified_at, verified_by
			  FROM archive_manifests
			 WHERE tenant_id = $1 AND partition = $2
			 ORDER BY exported_at DESC
			 LIMIT 1`, tenantID, partition)

		var verifiedBy *string
		if err := row.Scan(&m.TenantID, &m.ManifestID, &m.Partition, &m.PeriodStart,
			&m.PeriodEnd, &m.EventCount, &m.ChainHead, &m.Destination, &m.ExportedAt,
			&m.ExportedBy, &m.VerifiedAt, &verifiedBy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoManifest
			}
			return err
		}
		if verifiedBy != nil {
			m.VerifiedBy = *verifiedBy
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// MarkVerified records that somebody read the archive back and it matched.
//
// Separate from the export, and by design: an export says what was written and a
// verification says it can still be read. Recording them together would mean an archive
// is "verified" because the process that wrote it believed so.
func (s *PostgresStore) MarkVerified(ctx context.Context, tenantID, manifestID, by string,
	at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE archive_manifests
			   SET verified_at = $3, verified_by = $4
			 WHERE tenant_id = $1 AND manifest_id = $2`,
			tenantID, manifestID, at.UTC(), by)
		return err
	})
}

// PlaceHold binds a tenant's evidence, or one correlation within it.
func (s *PostgresStore) PlaceHold(ctx context.Context, h Hold) error {
	return s.withTenant(ctx, h.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO legal_holds
				(tenant_id, hold_id, correlation_id, reason, placed_by, placed_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, hold_id) DO NOTHING`,
			h.TenantID, h.HoldID, nullable(h.CorrelationID), h.Reason, h.PlacedBy,
			h.PlacedAt.UTC())
		return err
	})
}

// ReleaseHold lifts one, naming who lifted it. A release without an author is refused
// by the schema, because a hold that stopped binding for no recorded reason is the
// thing a hold exists to prevent.
func (s *PostgresStore) ReleaseHold(ctx context.Context, tenantID, holdID, by string,
	at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE legal_holds
			   SET released_at = $3, released_by = $4
			 WHERE tenant_id = $1 AND hold_id = $2 AND released_at IS NULL`,
			tenantID, holdID, at.UTC(), by)
		return err
	})
}

// ActiveHolds returns the holds still binding.
func (s *PostgresStore) ActiveHolds(ctx context.Context, tenantID string) ([]Hold, error) {
	var out []Hold
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, hold_id, correlation_id, reason, placed_by, placed_at
			  FROM legal_holds
			 WHERE tenant_id = $1 AND released_at IS NULL
			 ORDER BY placed_at ASC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var h Hold
			var correlation *string
			if err := rows.Scan(&h.TenantID, &h.HoldID, &correlation, &h.Reason,
				&h.PlacedBy, &h.PlacedAt); err != nil {
				return err
			}
			if correlation != nil {
				h.CorrelationID = *correlation
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
