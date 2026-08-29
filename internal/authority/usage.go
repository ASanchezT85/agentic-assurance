package authority

import (
	"context"
	"fmt"
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

	// State is where the reserved capacity ended up. RELEASED rows are capacity
	// returned and count against nothing.
	State ReservationState

	ClosedAt *time.Time
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

// Reserve is the in-process reservation: correct for one replica and wrong for
// several, exactly like the rest of this type. It exists so unit tests exercise the
// same call the gateway makes; the ceiling that has to hold across gateways is the
// PostgreSQL one.
func (m *MemoryUsage) Reserve(ctx context.Context, g *Grant, idempotencyKey string,
	notional float64, at time.Time) (Decision, error) {

	if g == nil {
		return Decision{}, fmt.Errorf("no grant to reserve against")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, held := m.entries[usageKey(g.TenantID, idempotencyKey)]; held {
		return allow(g, at.UTC()), nil
	}

	consumed := m.snapshot(g.TenantID, g.GrantID, at)
	if code, reason := checkLimits(g.Limits, consumed, notional); code != "" {
		return reservationDecision(g, at.UTC(), false, code, reason), nil
	}

	m.entries[usageKey(g.TenantID, idempotencyKey)] = &Entry{
		TenantID: g.TenantID, GrantID: g.GrantID, IdempotencyKey: idempotencyKey,
		Notional: notional, SubmittedAt: at.UTC(), Open: true, State: StateReserved,
	}
	return allow(g, at.UTC()), nil
}

// Settle resolves a held reservation.
func (m *MemoryUsage) Settle(_ context.Context, tenantID, idempotencyKey string,
	state ReservationState, open bool, at time.Time) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	entry, ok := m.entries[usageKey(tenantID, idempotencyKey)]
	if !ok {
		return nil
	}
	entry.State = state
	entry.Open = open
	if !open {
		closed := at.UTC()
		entry.ClosedAt = &closed
	}
	return nil
}

func (m *MemoryUsage) Usage(_ context.Context, tenantID, grantID string, now time.Time) (Snapshot, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.snapshot(tenantID, grantID, now), nil
}

// snapshot counts held and spent capacity. Callers hold the lock.
func (m *MemoryUsage) snapshot(tenantID, grantID string, now time.Time) Snapshot {
	now = now.UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var snap Snapshot
	for _, e := range m.entries {
		if e.TenantID != tenantID || e.GrantID != grantID || e.State == StateReleased {
			continue
		}
		if e.SubmittedAt.After(now.Add(-time.Hour)) {
			snap.Rolling1hNotional += e.Notional
		}
		if !e.SubmittedAt.Before(dayStart) {
			snap.DailyNotional += e.Notional
		}
		if e.Open {
			snap.OpenOrders++
		}
	}
	return snap
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

// Reserve holds capacity for one intent, atomically, or refuses.
//
// Everything that decides happens inside one transaction behind an advisory lock on the
// grant: the lock, the count, the arithmetic and the write. Two gateways deciding at
// the same instant serialise here, which is the difference between a ledger that does
// not lose writes and a ceiling that cannot be exceeded.
//
// The lock is per grant, so orders under different grants never wait for each other,
// and it is transaction-scoped, so it is released by commit or rollback rather than by
// remembering to.
func (s *PostgresUsage) Reserve(ctx context.Context, g *Grant, idempotencyKey string,
	notional float64, at time.Time) (Decision, error) {

	if g == nil {
		return Decision{}, fmt.Errorf("no grant to reserve against")
	}

	now := at.UTC()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	var decision Decision
	err := s.withTenant(ctx, g.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			"SELECT pg_advisory_xact_lock(hashtext($1))", g.TenantID+":"+g.GrantID); err != nil {
			return err
		}

		// A retry that already holds capacity keeps it. Counting it again would let a
		// duplicate submission spend the grant twice, which is the same defect the
		// idempotency record exists to prevent one layer down.
		var held bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM authority_usage
				 WHERE tenant_id = $1 AND idempotency_key = $2)`,
			g.TenantID, idempotencyKey).Scan(&held); err != nil {
			return err
		}
		if held {
			decision = allow(g, now)
			return nil
		}

		// Released rows are capacity returned: an order the venue definitively
		// refused never existed, and leaving it consumed would let anyone exhaust a
		// customer's grant with requests that were always going to be rejected.
		var consumed Snapshot
		if err := tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(notional) FILTER (WHERE submitted_at > $3), 0),
				COALESCE(SUM(notional) FILTER (WHERE submitted_at >= $4), 0),
				COUNT(*) FILTER (WHERE open)
			FROM authority_usage
			WHERE tenant_id = $1 AND grant_id = $2 AND state <> 'RELEASED'`,
			g.TenantID, g.GrantID, now.Add(-time.Hour), dayStart,
		).Scan(&consumed.Rolling1hNotional, &consumed.DailyNotional, &consumed.OpenOrders); err != nil {
			return err
		}

		if code, reason := checkLimits(g.Limits, consumed, notional); code != "" {
			decision = reservationDecision(g, now, false, code, reason)
			return nil
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO authority_usage
				(tenant_id, grant_id, idempotency_key, notional, submitted_at, open, state)
			VALUES ($1,$2,$3,$4,$5,true,$6)`,
			g.TenantID, g.GrantID, idempotencyKey, notional, now, string(StateReserved)); err != nil {
			return err
		}

		decision = allow(g, now)
		return nil
	})
	if err != nil {
		// Spec section 17: a limit that cannot be enforced denies. Nothing reaches a
		// venue without a committed reservation.
		return deny(g, now, "USAGE_UNAVAILABLE",
			"the reservation could not be committed: "+err.Error()), nil
	}
	return decision, nil
}

// Settle records what the venue did with reserved capacity.
func (s *PostgresUsage) Settle(ctx context.Context, tenantID, idempotencyKey string,
	state ReservationState, open bool, at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var closedAt *time.Time
		if !open {
			t := at.UTC()
			closedAt = &t
		}
		_, err := tx.Exec(ctx, `
			UPDATE authority_usage
			   SET state = $3, open = $4, closed_at = $5
			 WHERE tenant_id = $1 AND idempotency_key = $2`,
			tenantID, idempotencyKey, string(state), open, closedAt)
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
			WHERE tenant_id = $1 AND grant_id = $2 AND state <> 'RELEASED'`,
			tenantID, grantID, now.Add(-time.Hour), dayStart,
		).Scan(&snap.Rolling1hNotional, &snap.DailyNotional, &snap.OpenOrders)
	})
	return snap, err
}
