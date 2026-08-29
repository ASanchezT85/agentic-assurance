package authority

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/money"
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
	Notional       money.Amount
	SubmittedAt    time.Time

	// Open is false once the order reached a terminal state. MaxOpenOrders counts
	// exposure the platform still has at a venue, and a filled order is no longer
	// that.
	Open bool

	// State is where the reserved capacity ended up. RELEASED rows are capacity
	// returned and count against nothing.
	State ReservationState

	// What the capacity was reserved for. A repeated idempotency key is a retry only
	// if every one of these matches; anything else is a different intent wearing the
	// same key.
	EnvelopeID  string
	PrincipalID string
	AccountID   string

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
	notional money.Amount, who ReservationIdentity, at time.Time) (Decision, error) {

	if g == nil {
		return Decision{}, fmt.Errorf("no grant to reserve against")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if held, exists := m.entries[usageKey(g.TenantID, idempotencyKey)]; exists {
		if held.GrantID != g.GrantID || held.EnvelopeID != who.EnvelopeID ||
			held.PrincipalID != who.PrincipalID || held.AccountID != who.AccountID ||
			held.Notional != notional {
			return reservationDecision(g, at.UTC(), false, "RESERVATION_KEY_REUSED",
				"this idempotency key already holds capacity for a different request"), nil
		}
		return allow(g, at.UTC()), nil
	}

	consumed := m.snapshot(g.TenantID, g.GrantID, at)
	if code, reason := checkLimits(g.Limits, consumed, notional); code != "" {
		return reservationDecision(g, at.UTC(), false, code, reason), nil
	}

	m.entries[usageKey(g.TenantID, idempotencyKey)] = &Entry{
		TenantID: g.TenantID, GrantID: g.GrantID, IdempotencyKey: idempotencyKey,
		Notional: notional, SubmittedAt: at.UTC(), Open: true, State: StateReserved,
		EnvelopeID: who.EnvelopeID, PrincipalID: who.PrincipalID, AccountID: who.AccountID,
	}
	return allow(g, at.UTC()), nil
}

// Release drops a reservation nothing was sent for.
func (m *MemoryUsage) Release(_ context.Context, tenantID, idempotencyKey string,
	_ time.Time) error {

	m.mu.Lock()
	defer m.mu.Unlock()

	key := usageKey(tenantID, idempotencyKey)
	if entry, ok := m.entries[key]; ok && entry.State == StateReserved {
		delete(m.entries, key)
	}
	return nil
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
			snap.Rolling1hNotional = snap.Rolling1hNotional.Add(e.Notional)
		}
		if !e.SubmittedAt.Before(dayStart) {
			snap.DailyNotional = snap.DailyNotional.Add(e.Notional)
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
			e.TenantID, e.GrantID, e.IdempotencyKey, e.Notional.String(), e.SubmittedAt.UTC(),
			e.Open, closedAt)
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
	notional money.Amount, who ReservationIdentity, at time.Time) (Decision, error) {

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

		// A retry that already holds capacity keeps it — but only a retry.
		//
		// This used to answer "a row exists for this key" and return ALLOW. A key left
		// behind by a failure that never reached a venue could then authorize a
		// different envelope, a different grant and a different amount, and an
		// idempotency record pruned by retention left a row that made a fresh request
		// invisible to rolling accounting. The identity is what tells a retry from a
		// different intent wearing the same key.
		var (
			held          bool
			heldGrant     string
			heldEnvelope  string
			heldPrincipal string
			heldAccount   string
			heldNotional  string
			heldState     string
		)
		err := tx.QueryRow(ctx, `
			SELECT grant_id, COALESCE(envelope_id, ''), COALESCE(principal_id, ''),
			       COALESCE(account_id, ''), notional::text, state
			  FROM authority_usage
			 WHERE tenant_id = $1 AND idempotency_key = $2`,
			g.TenantID, idempotencyKey).Scan(&heldGrant, &heldEnvelope, &heldPrincipal,
			&heldAccount, &heldNotional, &heldState)
		switch {
		case err == nil:
			held = true
		case errors.Is(err, pgx.ErrNoRows):
			held = false
		default:
			return err
		}

		if held {
			mismatch := ""
			switch {
			case heldGrant != g.GrantID:
				mismatch = "a different authority grant"
			case heldEnvelope != who.EnvelopeID:
				mismatch = "a different envelope"
			case heldPrincipal != who.PrincipalID || heldAccount != who.AccountID:
				mismatch = "a different principal or account"
			case !sameAmount(heldNotional, notional):
				mismatch = "a different amount"
			}
			if mismatch != "" {
				decision = reservationDecision(g, now, false, "RESERVATION_KEY_REUSED",
					"this idempotency key already holds capacity for "+mismatch+
						"; a key identifies one economic request, and inheriting that "+
						"reservation would authorize an amount nobody evaluated (INV-002)")
				return nil
			}
			// The same request again. It keeps the capacity it already holds, which is
			// what makes a retry safe.
			decision = allow(g, now)
			return nil
		}

		// Released rows are capacity returned: an order the venue definitively
		// refused never existed, and leaving it consumed would let anyone exhaust a
		// customer's grant with requests that were always going to be rejected.
		var (
			consumed Snapshot
			rolling  string
			daily    string
		)
		if err := tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(notional) FILTER (WHERE submitted_at > $3), 0)::text,
				COALESCE(SUM(notional) FILTER (WHERE submitted_at >= $4), 0)::text,
				COUNT(*) FILTER (WHERE open)
			FROM authority_usage
			WHERE tenant_id = $1 AND grant_id = $2 AND state <> 'RELEASED'`,
			g.TenantID, g.GrantID, now.Add(-time.Hour), dayStart,
		).Scan(&rolling, &daily, &consumed.OpenOrders); err != nil {
			return err
		}
		// Text, not a float. A sum that travelled through a float64 would be a number
		// the ceiling was compared against but not the number that was stored.
		//
		// An unreadable sum aborts the reservation. Consumed usage that cannot be read
		// is not consumed usage of zero.
		var parseErr error
		if consumed.Rolling1hNotional, parseErr = parseAmount(rolling); parseErr != nil {
			return parseErr
		}
		if consumed.DailyNotional, parseErr = parseAmount(daily); parseErr != nil {
			return parseErr
		}

		if code, reason := checkLimits(g.Limits, consumed, notional); code != "" {
			decision = reservationDecision(g, now, false, code, reason)
			return nil
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO authority_usage
				(tenant_id, grant_id, idempotency_key, notional, submitted_at, open, state,
				 envelope_id, principal_id, account_id)
			VALUES ($1,$2,$3,$4,$5,true,$6,$7,$8,$9)`,
			g.TenantID, g.GrantID, idempotencyKey, notional.String(), now, string(StateReserved),
			who.EnvelopeID, who.PrincipalID, who.AccountID); err != nil {
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

// Release deletes a reservation when it is known that nothing was sent.
//
// Deleted rather than marked RELEASED: a released row is capacity returned either way,
// and removing it means the key is genuinely free for a later, properly evaluated
// request. Marking would leave exactly the stale row this whole change exists to stop
// being reusable.
func (s *PostgresUsage) Release(ctx context.Context, tenantID, idempotencyKey string,
	at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			DELETE FROM authority_usage
			 WHERE tenant_id = $1 AND idempotency_key = $2 AND state = $3`,
			tenantID, idempotencyKey, string(StateReserved))
		return err
	})
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

	var (
		snap    Snapshot
		rolling string
		daily   string
	)
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		// One round trip. Three queries would let the rolling window and the daily
		// window disagree about what "now" was.
		return tx.QueryRow(ctx, `
			SELECT
				COALESCE(SUM(notional) FILTER (WHERE submitted_at > $3), 0)::text,
				COALESCE(SUM(notional) FILTER (WHERE submitted_at >= $4), 0)::text,
				COUNT(*) FILTER (WHERE open)
			FROM authority_usage
			WHERE tenant_id = $1 AND grant_id = $2 AND state <> 'RELEASED'`,
			tenantID, grantID, now.Add(-time.Hour), dayStart,
		).Scan(&rolling, &daily, &snap.OpenOrders)
	})
	if err != nil {
		return snap, err
	}
	if snap.Rolling1hNotional, err = parseAmount(rolling); err != nil {
		return Snapshot{}, err
	}
	if snap.DailyNotional, err = parseAmount(daily); err != nil {
		return Snapshot{}, err
	}
	return snap, nil
}

// parseAmount reads a PostgreSQL numeric that arrived as text.
//
// It returns the error rather than swallowing it. It used to return zero on a parse
// failure, and zero means "nothing consumed": malformed authoritative state would have
// read as a grant with its full capacity available, which is the one direction a ceiling
// must never fail in. The condition is unlikely — the column is numeric(20,4) and
// everything written to it comes from Amount.String() — and "unlikely" is not a property
// a limit can be built on.
//
// The caller turns this into USAGE_UNAVAILABLE and denies.
// sameAmount compares a stored amount with the one being reserved.
//
// An unreadable stored amount is not equal to anything: the reservation is refused as a
// key reuse rather than treated as a match, because "we cannot read what this key holds"
// must not resolve to "it holds what you are asking for".
func sameAmount(text string, notional money.Amount) bool {
	held, err := parseAmount(text)
	return err == nil && held == notional
}

func parseAmount(text string) (money.Amount, error) {
	amount, err := money.Parse(text)
	if err != nil {
		return 0, fmt.Errorf("consumed usage %q is not a readable amount: %w", text, err)
	}
	return amount, nil
}
