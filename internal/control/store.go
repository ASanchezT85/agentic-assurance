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
				applied_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (tenant_id, control_id) DO NOTHING`,
			c.TenantID, c.ControlID, c.IncidentID, string(c.Action),
			nullable(c.AgentID), nullable(c.AccountID), c.CohortID,
			c.AuthorizedBy, c.PolicyBundleID, c.Reason, c.AppliedAt, c.ExpiresAt)
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
			       authorized_by, policy_bundle_id, reason, applied_at, expires_at
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
			if err := rows.Scan(&c.ControlID, &c.TenantID, &c.IncidentID, &action,
				&c.AgentID, &c.AccountID, &c.CohortID, &c.AuthorizedBy,
				&c.PolicyBundleID, &c.Reason, &c.AppliedAt, &c.ExpiresAt); err != nil {
				return err
			}
			c.Action = fleet.ControlAction(action)
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
			       revoked_at, COALESCE(revoked_by, '')
			FROM fleet_controls
			ORDER BY applied_at DESC`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var c Control
			var action string
			if err := rows.Scan(&c.ControlID, &c.TenantID, &c.IncidentID, &action,
				&c.AgentID, &c.AccountID, &c.CohortID, &c.AuthorizedBy,
				&c.PolicyBundleID, &c.Reason, &c.AppliedAt, &c.ExpiresAt,
				&c.RevokedAt, &c.RevokedBy); err != nil {
				return err
			}
			c.Action = fleet.ControlAction(action)
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
