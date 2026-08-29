package authority

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/intent"
)

// ErrGrantNotFound is returned when no grant matches. It is deliberately not
// distinguishable from "exists but belongs to another tenant": telling a caller that
// a grant id exists in some other tenant is itself a cross-tenant disclosure.
var ErrGrantNotFound = errors.New("authority grant not found")

// ErrTenantContextMissing is returned when a query is attempted without a tenant.
// Row level security would return zero rows, which reads like "not found" and hides
// the real bug.
var ErrTenantContextMissing = errors.New("no tenant in context")

// Store is the PostgreSQL-backed grant repository.
//
// Every read and write runs inside a transaction that sets app.tenant_id, which is
// what the row level security policy keys on. There is no method that reads without
// a tenant, because a repository with one such method has no tenant isolation.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// withTenant runs fn inside a transaction scoped to one tenant.
//
// set_config with is_local = true ties the setting to the transaction, so it cannot
// leak to the next user of a pooled connection. That leak is the standard way RLS
// quietly stops isolating anything.
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

const grantColumns = `grant_id, tenant_id, principal_id, account_id, agent_id,
	issued_at, valid_from, valid_until,
	allowed_operations, allowed_asset_classes, allowed_instruments, denied_instruments,
	per_order_notional, rolling_1h_notional, daily_notional, max_open_orders,
	status, revoked_at, revocation_reason`

// Load fetches one grant within a tenant.
func (s *Store) Load(ctx context.Context, tenantID, grantID string) (*Grant, error) {
	var g *Grant
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT `+grantColumns+` FROM authority_grants WHERE grant_id = $1`, grantID)
		loaded, err := scanGrant(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGrantNotFound
		}
		if err != nil {
			return err
		}
		g = loaded
		return nil
	})
	return g, err
}

// Save inserts or updates a grant. The tenant comes from the grant itself and from
// the RLS context, and PostgreSQL rejects a mismatch through the policy's WITH CHECK.
func (s *Store) Save(ctx context.Context, g *Grant) error {
	return s.withTenant(ctx, g.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO authority_grants (`+grantColumns+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
			ON CONFLICT (tenant_id, grant_id) DO UPDATE SET
				valid_until = EXCLUDED.valid_until,
				allowed_operations = EXCLUDED.allowed_operations,
				allowed_asset_classes = EXCLUDED.allowed_asset_classes,
				allowed_instruments = EXCLUDED.allowed_instruments,
				denied_instruments = EXCLUDED.denied_instruments,
				per_order_notional = EXCLUDED.per_order_notional,
				rolling_1h_notional = EXCLUDED.rolling_1h_notional,
				daily_notional = EXCLUDED.daily_notional,
				max_open_orders = EXCLUDED.max_open_orders,
				status = EXCLUDED.status,
				revoked_at = EXCLUDED.revoked_at,
				revocation_reason = EXCLUDED.revocation_reason`,
			g.GrantID, g.TenantID, g.PrincipalID, g.AccountID, g.AgentID,
			g.IssuedAt.UTC(), g.ValidFrom.UTC(), g.ValidUntil.UTC(),
			sidesToText(g.AllowedOperations), assetClassesToText(g.AllowedAssetClasses),
			emptyIfNil(g.AllowedInstruments), emptyIfNil(g.DeniedInstruments),
			g.Limits.PerOrderNotional, g.Limits.Rolling1hNotional,
			g.Limits.DailyNotional, g.Limits.MaxOpenOrders,
			string(g.Status), g.RevokedAt, nullIfEmpty(g.RevocationReason))
		return err
	})
}

// Revoke marks a grant revoked and records why.
//
// It updates rather than deletes: a decision taken under a grant stays explainable
// only if the grant it was taken under still exists (ADR-009).
func (s *Store) Revoke(ctx context.Context, tenantID, grantID string, at time.Time, reason string) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE authority_grants
			   SET status = 'REVOKED', revoked_at = $2, revocation_reason = $3
			 WHERE grant_id = $1 AND status = 'ACTIVE'`,
			grantID, at.UTC(), nullIfEmpty(reason))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Either it does not exist, is not ours, or is already revoked. All
			// three leave the caller with a revoked-or-absent grant, which is the
			// outcome they asked for.
			return ErrGrantNotFound
		}
		return nil
	})
}

func scanGrant(row pgx.Row) (*Grant, error) {
	var (
		g         Grant
		ops       []string
		classes   []string
		status    string
		revokedAt *time.Time
		revReason *string
	)
	err := row.Scan(
		&g.GrantID, &g.TenantID, &g.PrincipalID, &g.AccountID, &g.AgentID,
		&g.IssuedAt, &g.ValidFrom, &g.ValidUntil,
		&ops, &classes, &g.AllowedInstruments, &g.DeniedInstruments,
		&g.Limits.PerOrderNotional, &g.Limits.Rolling1hNotional,
		&g.Limits.DailyNotional, &g.Limits.MaxOpenOrders,
		&status, &revokedAt, &revReason,
	)
	if err != nil {
		return nil, err
	}

	for _, o := range ops {
		g.AllowedOperations = append(g.AllowedOperations, intent.Side(o))
	}
	for _, c := range classes {
		g.AllowedAssetClasses = append(g.AllowedAssetClasses, intent.AssetClass(c))
	}
	g.Status = Status(status)
	g.RevokedAt = revokedAt
	if revReason != nil {
		g.RevocationReason = *revReason
	}
	return &g, nil
}

func sidesToText(in []intent.Side) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}

func assetClassesToText(in []intent.AssetClass) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, string(c))
	}
	return out
}

func emptyIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
