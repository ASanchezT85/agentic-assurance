package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// KeyStore reads and registers agent signing keys.
//
// Every read is tenant-scoped inside a transaction that sets app.tenant_id, like every
// other store here: a key registry that could be read without a tenant would be the one
// place INV-007 does not hold, and it holds the material that decides whose agent an
// envelope belongs to.
type KeyStore struct {
	pool *pgxpool.Pool
}

func NewKeyStore(pool *pgxpool.Pool) *KeyStore { return &KeyStore{pool: pool} }

func (s *KeyStore) withTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return fmt.Errorf("no tenant in context")
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

// AgentKey returns the key registered for exactly this tenant, agent and key id.
//
// All three, always. Looking up by key id alone would let a key registered for one
// agent verify another's envelope, which is the binding the registry exists to make.
func (s *KeyStore) AgentKey(ctx Context, tenantID, agentID, keyID string) (*AgentKey, error) {
	standard, ok := ctx.(context.Context)
	if !ok {
		standard = context.Background()
	}

	var key *AgentKey
	err := s.withTenant(standard, tenantID, func(tx pgx.Tx) error {
		var (
			found      AgentKey
			validUntil *time.Time
			revokedAt  *time.Time
		)
		err := tx.QueryRow(standard, `
			SELECT tenant_id, agent_id, key_id, algorithm, public_key, status,
			       valid_from, valid_until, revoked_at
			  FROM agent_signing_keys
			 WHERE agent_id = $1 AND key_id = $2`, agentID, keyID).
			Scan(&found.TenantID, &found.AgentID, &found.KeyID, &found.Algorithm,
				&found.PublicKey, &found.Status, &found.ValidFrom, &validUntil, &revokedAt)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if validUntil != nil {
			found.ValidUntil = *validUntil
		}
		found.RevokedAt = revokedAt
		key = &found
		return nil
	})
	return key, err
}

// Register adds a verification key.
func (s *KeyStore) Register(ctx context.Context, k AgentKey) error {
	return s.withTenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		validUntil := any(nil)
		if !k.ValidUntil.IsZero() {
			validUntil = k.ValidUntil.UTC()
		}
		status := k.Status
		if status == "" {
			status = "ACTIVE"
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO agent_signing_keys
				(tenant_id, agent_id, key_id, algorithm, public_key, status, valid_from, valid_until)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, agent_id, key_id) DO NOTHING`,
			k.TenantID, k.AgentID, k.KeyID, k.Algorithm, k.PublicKey, status,
			k.ValidFrom.UTC(), validUntil)
		return err
	})
}

// Revoke stops a key verifying, from a moment on.
//
// A revoked key is kept rather than deleted: an envelope signed last week was signed by
// a key that was valid last week, and an evidence chain that referenced a row nobody
// can find any more would be unreadable exactly when it matters.
func (s *KeyStore) Revoke(ctx context.Context, tenantID, agentID, keyID, revokedBy string,
	at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE agent_signing_keys
			   SET status = 'REVOKED', revoked_at = $4, revoked_by = $5
			 WHERE agent_id = $1 AND key_id = $2 AND tenant_id = $3`,
			agentID, keyID, tenantID, at.UTC(), revokedBy)
		return err
	})
}
