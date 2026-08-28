package authority

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Consumed usage is recorded against a grant when an order is actually submitted.
//
// It is a separate ledger rather than a query over idempotency records, because those
// carry no grant and no notional: they answer "did we already submit this", not "how
// much has this grant spent". Deriving one from the other would mean joining against
// data that does not exist.
//
// Usage is recorded at submission, not at fill. A grant that caps an hour of exposure
// is capping what was committed, and an order sitting open at a venue is committed
// whether or not it has filled.

// Entry is one authorized submission.
type Entry struct {
	TenantID       string
	GrantID        string
	IdempotencyKey string
	Notional       float64
	SubmittedAt    time.Time

	// Open is false once the order reached a terminal state. MaxOpenOrders counts
	// exposure the platform still has at a venue, and a filled order is no longer
	// that.
	Open bool
}

// Recorder is written to after a submission. Reading and writing are separate
// interfaces because the evaluator must only ever read: a component that could record
// its own usage could also record none.
type Recorder interface {
	Record(ctx context.Context, e Entry) error
	Close(ctx context.Context, tenantID, idempotencyKey string, at time.Time) error
}

// MemoryUsage is an in-process ledger.
//
// Correct for one replica and wrong for several, which is the whole reason the
// PostgreSQL implementation exists: two gateways each enforcing half a rolling limit
// enforce no rolling limit at all.
type MemoryUsage struct {
	mu      sync.RWMutex
	entries map[string]*Entry
}

func NewMemoryUsage() *MemoryUsage {
	return &MemoryUsage{entries: map[string]*Entry{}}
}

func usageKey(tenantID, idempotencyKey string) string { return tenantID + "\x00" + idempotencyKey }

func (m *MemoryUsage) Record(_ context.Context, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Keyed by idempotency key, so a replayed submission does not spend the grant
	// twice.
	k := usageKey(e.TenantID, e.IdempotencyKey)
	if _, exists := m.entries[k]; exists {
		return nil
	}
	copied := e
	m.entries[k] = &copied
	return nil
}

func (m *MemoryUsage) Close(_ context.Context, tenantID, idempotencyKey string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[usageKey(tenantID, idempotencyKey)]; ok {
		e.Open = false
	}
	return nil
}

func (m *MemoryUsage) Usage(_ context.Context, tenantID, grantID string, now time.Time) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now = now.UTC()
	hourAgo := now.Add(-time.Hour)
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var s Snapshot
	for _, e := range m.entries {
		if e.TenantID != tenantID || e.GrantID != grantID {
			continue
		}
		if e.SubmittedAt.After(hourAgo) {
			s.Rolling1hNotional += e.Notional
		}
		if !e.SubmittedAt.Before(dayStart) {
			s.DailyNotional += e.Notional
		}
		if e.Open {
			s.OpenOrders++
		}
	}
	return s, nil
}

// PostgresUsage is the ledger the hot path uses.
type PostgresUsage struct{ pool *pgxpool.Pool }

func NewPostgresUsage(pool *pgxpool.Pool) *PostgresUsage { return &PostgresUsage{pool: pool} }

func (s *PostgresUsage) withTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Row level security is enforced by the database, not by the query. A missing
	// WHERE clause must not become a cross-tenant read (INV-007).
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresUsage) Record(ctx context.Context, e Entry) error {
	return s.withTenant(ctx, e.TenantID, func(tx pgx.Tx) error {
		// An order that was already terminal when we recorded it is closed at the
		// same instant. The table requires it: a row that is not open and has no
		// closed_at cannot say when the exposure ended.
		var closedAt *time.Time
		if !e.Open {
			t := e.SubmittedAt.UTC()
			closedAt = &t
		}

		// ON CONFLICT DO NOTHING: a replayed submission must not spend the grant a
		// second time.
		_, err := tx.Exec(ctx, `
			INSERT INTO authority_usage
				(tenant_id, grant_id, idempotency_key, notional, submitted_at, open, closed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
			e.TenantID, e.GrantID, e.IdempotencyKey, e.Notional, e.SubmittedAt.UTC(), e.Open, closedAt)
		return err
	})
}

func (s *PostgresUsage) Close(ctx context.Context, tenantID, idempotencyKey string, at time.Time) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE authority_usage SET open = false, closed_at = $3
			WHERE tenant_id = $1 AND idempotency_key = $2`,
			tenantID, idempotencyKey, at.UTC())
		return err
	})
}

func (s *PostgresUsage) Usage(ctx context.Context, tenantID, grantID string, now time.Time) (Snapshot, error) {
	now = now.UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var snap Snapshot
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		// One round trip. Three queries would let the rolling window and the daily
		// window disagree about what "now" was.
		return tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(notional) FILTER (WHERE submitted_at > $3), 0),
				COALESCE(SUM(notional) FILTER (WHERE submitted_at >= $4), 0),
				COUNT(*) FILTER (WHERE open)
			FROM authority_usage
			WHERE tenant_id = $1 AND grant_id = $2`,
			tenantID, grantID, now.Add(-time.Hour), dayStart,
		).Scan(&snap.Rolling1hNotional, &snap.DailyNotional, &snap.OpenOrders)
	})
	return snap, err
}
