package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/fleet"
)

// ErrTenantContextMissing is returned when a query is attempted without a tenant. Row
// level security would return zero rows, which reads like "no controls are in force"
// and would silently unenforce every one of them.
var ErrTenantContextMissing = errors.New("no tenant in context")

// ErrControlExists is returned when a control id is already taken.
var ErrControlExists = errors.New("a control with this id already exists")

// Store is the PostgreSQL-backed control repository.
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

// Save stores a newly authorized control.
//
// It refuses a repeated id rather than updating. A control is the record of a decision
// a named person made at a moment; editing one in place would let its scope or its
// expiry move without anyone having authorized the move.
func (s *Store) Save(ctx context.Context, c Control) error {
	return s.withTenant(ctx, c.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO fleet_controls (
				tenant_id, control_id, incident_id, action, agent_id, account_id,
				cohort_id, authorized_by, policy_bundle_id, reason,
				applied_at, expires_at, max_orders, window_seconds, agent_ids)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (tenant_id, control_id) DO NOTHING`,
			c.TenantID, c.ControlID, c.IncidentID, string(c.Action),
			nullable(c.AgentID), nullable(c.AccountID), c.CohortID,
			c.AuthorizedBy, c.PolicyBundleID, c.Reason, c.AppliedAt, c.ExpiresAt,
			nullableInt(c.MaxOrders), nullableInt(int(c.Window.Seconds())),
			nullableList(c.AgentIDs))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrControlExists
		}
		return nil
	})
}

// InForce returns the controls that still bind for a tenant.
//
// Expired and revoked ones are filtered in SQL rather than in Go, because this runs on
// every submission and a caller that forgot the filter would enforce a control that
// ended last month.
func (s *Store) InForce(ctx context.Context, tenantID string, at time.Time) ([]Control, error) {
	var out []Control
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT control_id, tenant_id, incident_id, action,
			       COALESCE(agent_id, ''), COALESCE(account_id, ''), cohort_id,
			       authorized_by, policy_bundle_id, reason, applied_at, expires_at,
			       COALESCE(max_orders, 0), COALESCE(window_seconds, 0),
			       COALESCE(agent_ids, ARRAY[]::text[])
			FROM fleet_controls
			WHERE revoked_at IS NULL AND expires_at > $1
			ORDER BY applied_at`, at.UTC())
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var c Control
			var action string
			var windowSeconds int
			if err := rows.Scan(&c.ControlID, &c.TenantID, &c.IncidentID, &action,
				&c.AgentID, &c.AccountID, &c.CohortID, &c.AuthorizedBy,
				&c.PolicyBundleID, &c.Reason, &c.AppliedAt, &c.ExpiresAt,
				&c.MaxOrders, &windowSeconds, &c.AgentIDs); err != nil {
				return err
			}
			c.Action = fleet.ControlAction(action)
			c.Window = time.Duration(windowSeconds) * time.Second
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ErrControlNotFound is returned when no control matches. Deliberately the same for a
// control that belongs to another tenant: telling a caller that an id exists elsewhere
// is itself a cross-tenant disclosure.
var ErrControlNotFound = errors.New("control not found")

// Revoke lifts a control before it expires, and reports whether it was already lifted.
//
// The lever this store was missing. A tenant-wide READ_ONLY control refuses every
// order in the tenant, and until this existed the only way to lift one was a psql
// prompt or waiting for expiry — during exactly the incident where that is the worst
// way to work, and impossible for an operator without database access.
func (s *Store) Revoke(ctx context.Context, tenantID, controlID, revokedBy string,
	at time.Time) (already bool, err error) {

	err = s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var revoked *time.Time
		row := tx.QueryRow(ctx,
			`SELECT revoked_at FROM fleet_controls WHERE control_id = $1 FOR UPDATE`, controlID)
		if scanErr := row.Scan(&revoked); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return ErrControlNotFound
			}
			return scanErr
		}
		if revoked != nil {
			already = true
			return nil
		}
		_, execErr := tx.Exec(ctx,
			`UPDATE fleet_controls SET revoked_at = $1, revoked_by = $2 WHERE control_id = $3`,
			at.UTC(), revokedBy, controlID)
		return execErr
	})
	return already, err
}

// List returns every control for a tenant, in force or not.
//
// Not only the ones in force: a denial names a control id, and an operator asking
// "what is this" about one that expired an hour ago deserves an answer rather than an
// empty list that reads like the id was invented.
func (s *Store) List(ctx context.Context, tenantID string) ([]Control, error) {
	var out []Control
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT control_id, tenant_id, incident_id, action,
			       COALESCE(agent_id, ''), COALESCE(account_id, ''), cohort_id,
			       authorized_by, policy_bundle_id, reason, applied_at, expires_at,
			       revoked_at, COALESCE(revoked_by, ''),
			       COALESCE(max_orders, 0), COALESCE(window_seconds, 0),
			       COALESCE(agent_ids, ARRAY[]::text[])
			FROM fleet_controls
			ORDER BY applied_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var c Control
			var action string
			var windowSeconds int
			if err := rows.Scan(&c.ControlID, &c.TenantID, &c.IncidentID, &action,
				&c.AgentID, &c.AccountID, &c.CohortID, &c.AuthorizedBy,
				&c.PolicyBundleID, &c.Reason, &c.AppliedAt, &c.ExpiresAt,
				&c.RevokedAt, &c.RevokedBy, &c.MaxOrders, &windowSeconds,
				&c.AgentIDs); err != nil {
				return err
			}
			c.Action = fleet.ControlAction(action)
			c.Window = time.Duration(windowSeconds) * time.Second
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

func nullableList(v []string) any {
	if len(v) == 0 {
		return nil
	}
	return v
}

func nullableInt(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}

// Consume takes one slot in a throttle's window, or reports that there is none.
//
// Counting and writing happen inside one transaction behind an advisory lock keyed on
// the control. Two callers that each read the count, saw room and then wrote would
// both pass, and a rate limit that only approximately holds under load is one that
// fails exactly when it matters — which for a containment control is during the event
// it was authorized for.
//
// The lock is per control, so throttled scopes serialise against themselves and
// against nothing else. Everything not throttled never reaches this code.
func (s *Store) Consume(ctx context.Context, tenantID string, c Control,
	idempotencyKey string, at time.Time) (allowed bool, used int, err error) {

	err = s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if _, lockErr := tx.Exec(ctx,
			"SELECT pg_advisory_xact_lock(hashtext($1))", tenantID+":"+c.ControlID); lockErr != nil {
			return lockErr
		}

		// A replay already holds its slot. Counting it again would let a duplicate
		// submission spend the window twice, which is how a throttle turns into a
		// smaller throttle for anyone who retries.
		var replay bool
		if scanErr := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM fleet_control_usage
				WHERE control_id = $1 AND idempotency_key = $2)`,
			c.ControlID, idempotencyKey).Scan(&replay); scanErr != nil {
			return scanErr
		}

		since := at.Add(-c.Window)
		if scanErr := tx.QueryRow(ctx, `
			SELECT count(*) FROM fleet_control_usage
			WHERE control_id = $1 AND submitted_at > $2`,
			c.ControlID, since).Scan(&used); scanErr != nil {
			return scanErr
		}

		if replay {
			allowed = true
			return nil
		}
		if used >= c.MaxOrders {
			allowed = false
			return nil
		}

		if _, execErr := tx.Exec(ctx, `
			INSERT INTO fleet_control_usage (tenant_id, control_id, idempotency_key, submitted_at)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT DO NOTHING`,
			tenantID, c.ControlID, idempotencyKey, at.UTC()); execErr != nil {
			return execErr
		}

		// And forget what the window has already left behind.
		//
		// One row per order a throttle allows, kept forever, is a table that grows with
		// traffic and is only ever read for the last few minutes of it. A throttle on a
		// busy tenant would leave millions of rows nobody queries, and the count that
		// enforces the limit would get slower as the control aged.
		//
		// Here rather than in a scheduled job: the lock is already held, the rows are
		// this control's, and a retention job is one more thing that can silently stop
		// running. Two windows back rather than one, so a clock that steps backwards
		// slightly does not delete a slot still being counted.
		if _, execErr := tx.Exec(ctx, `
			DELETE FROM fleet_control_usage
			WHERE control_id = $1 AND submitted_at < $2`,
			c.ControlID, at.Add(-2*c.Window).UTC()); execErr != nil {
			return execErr
		}
		allowed = true
		used++
		return nil
	})
	return allowed, used, err
}

// Forget drops a throttle's usage. Called when a control is revoked, so a scope that
// was throttled and released does not carry a spent window into the next incident.
func (s *Store) Forget(ctx context.Context, tenantID, controlID string) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			"DELETE FROM fleet_control_usage WHERE control_id = $1", controlID)
		return err
	})
}
