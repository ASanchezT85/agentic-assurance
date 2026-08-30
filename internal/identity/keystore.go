package identity

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"agentic-assurance/internal/evidence"
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
// Register records a public key for an agent, and says whether it did.
//
// The returned bool is the point. The insert does nothing when the key id already
// exists — which is the security property, because replacing a key would let whoever
// can register one take over an agent that already has a grant — and a caller told
// only "no error" would believe their key is live when the one in force is somebody
// else's. Rotation is a new key id and a revocation of the old, never an overwrite.
func (s *KeyStore) Register(ctx context.Context, k AgentKey) (registered bool, err error) {
	err = s.withTenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		validUntil := any(nil)
		if !k.ValidUntil.IsZero() {
			validUntil = k.ValidUntil.UTC()
		}
		status := k.Status
		if status == "" {
			status = "ACTIVE"
		}
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO agent_signing_keys
				(tenant_id, agent_id, key_id, algorithm, public_key, status, valid_from, valid_until)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, agent_id, key_id) DO NOTHING`,
			k.TenantID, k.AgentID, k.KeyID, k.Algorithm, k.PublicKey, status,
			k.ValidFrom.UTC(), validUntil)
		if execErr != nil {
			return execErr
		}
		registered = tag.RowsAffected() == 1
		return nil
	})
	return registered, err
}

// RegisterWithEvidence registers a key and records that it was registered, in one
// transaction.
//
// Binding a public key to an agent is the moment that key becomes able to act. The
// evidence write used to be best effort — appended after the fact and its error
// discarded — so a registration could commit while the record of it did not, leaving the
// platform trusting a key nothing in the evidence chain accounts for. An investigation
// asking "when did this key become able to sign for this agent, and who said so" would
// find nothing (F4-K006).
func (s *KeyStore) RegisterWithEvidence(ctx context.Context, k AgentKey,
	event evidence.Event) (registered bool, err error) {

	err = s.withTenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		validUntil := any(nil)
		if !k.ValidUntil.IsZero() {
			validUntil = k.ValidUntil.UTC()
		}
		status := k.Status
		if status == "" {
			status = "ACTIVE"
		}
		tag, execErr := tx.Exec(ctx, `
			INSERT INTO agent_signing_keys
				(tenant_id, agent_id, key_id, algorithm, public_key, status, valid_from, valid_until)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, agent_id, key_id) DO NOTHING`,
			k.TenantID, k.AgentID, k.KeyID, k.Algorithm, k.PublicKey, status,
			k.ValidFrom.UTC(), validUntil)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() != 1 {
			// The key id is taken. Nothing is registered, so nothing is recorded.
			registered = false
			return nil
		}
		registered = true
		return evidence.AppendInTx(ctx, tx, event)
	})
	if err != nil {
		return false, err
	}
	return registered, nil
}

// Revoke stops a key verifying, from a moment on.
//
// A revoked key is kept rather than deleted: an envelope signed last week was signed by
// a key that was valid last week, and an evidence chain that referenced a row nobody
// can find any more would be unreadable exactly when it matters.
// It reports whether it revoked anything. It used to discard RowsAffected and return nil
// for an unknown tenant, agent or key, so revoking a key that did not exist answered 200
// and wrote an evidence event saying a key had been revoked. An operator who mistyped an
// agent id was told the key was stopped while the key they meant was still trusted, and
// the audit trail agreed with them.
func (s *KeyStore) Revoke(ctx context.Context, tenantID, agentID, keyID, revokedBy string,
	at time.Time) (bool, error) {

	revoked := false
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE agent_signing_keys
			   SET status = 'REVOKED', revoked_at = $4, revoked_by = $5
			 WHERE agent_id = $1 AND key_id = $2 AND tenant_id = $3
			   AND status <> 'REVOKED'`,
			agentID, keyID, tenantID, at.UTC(), revokedBy)
		if err != nil {
			return err
		}
		revoked = tag.RowsAffected() == 1
		return nil
	})
	return revoked, err
}

// Exists reports whether a key is registered at all, revoked or not.
//
// It is what separates "already revoked" from "no such key": the first is an idempotent
// no-op and the second is very likely a typo in an agent id, at a moment when somebody
// believes they have just contained a compromise.
func (s *KeyStore) Exists(ctx context.Context, tenantID, agentID, keyID string) (bool, error) {
	var exists bool
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM agent_signing_keys
				 WHERE tenant_id = $1 AND agent_id = $2 AND key_id = $3)`,
			tenantID, agentID, keyID).Scan(&exists)
	})
	return exists, err
}
