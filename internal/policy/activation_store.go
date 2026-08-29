package policy

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/evidence"
)

// The durable half of policy activation.
//
// Which bundle is in force used to be remembered by whichever process had witnessed the
// change, so a restart recorded the bundle it read at startup as a fresh activation the
// customer never performed, and two replicas could disagree about the history of one
// tenant. An evidence timeline must describe customer actions rather than process
// observations mistaken for them.
//
// A transition and its evidence are written in one transaction. Enforcement changes only
// after that transaction commits, so there is no state where the platform enforces a
// policy whose authorization was never recorded — the previous design appended evidence
// after switching, and discarded the error.

// ActivationKey is a key that may authorize an activation.
type ActivationKey struct {
	TenantID   string
	KeyID      string
	Algorithm  string
	PublicKey  ed25519.PublicKey
	Holder     string
	Status     string
	ValidFrom  time.Time
	ValidUntil *time.Time
	RevokedAt  *time.Time
	RevokedBy  string
}

// Usable reports whether the key may authorize at this instant.
func (k ActivationKey) Usable(at time.Time) error {
	switch {
	case k.Status != "ACTIVE" || k.RevokedAt != nil:
		return activationErr("ACTIVATION_KEY_REVOKED",
			"key %s is revoked; a revoked key cannot authorize a policy change", k.KeyID)
	case at.Before(k.ValidFrom):
		return activationErr("ACTIVATION_KEY_NOT_YET_VALID",
			"key %s is not valid until %s", k.KeyID, k.ValidFrom.Format(time.RFC3339))
	case k.ValidUntil != nil && !at.Before(*k.ValidUntil):
		return activationErr("ACTIVATION_KEY_EXPIRED",
			"key %s expired at %s", k.KeyID, k.ValidUntil.Format(time.RFC3339))
	}
	return nil
}

// Transition is an accepted activation, as it is stored.
type Transition struct {
	TenantID          string
	Nonce             string
	BundleID          string
	BundleContentHash string
	PriorBundleID     string
	Action            ActivationAction
	Actor             string
	Reason            string
	KeyID             string
	AuthorizedAt      time.Time
	AcceptedAt        time.Time
}

// ErrNoTransition means this tenant has never had a policy activated.
var ErrNoTransition = errors.New("no policy activation has been accepted for this tenant")

// ErrReplayed means the nonce has been seen. Distinct from any other conflict because it
// is the one that matters: a captured authorization presented a second time.
var ErrReplayed = errors.New("this authorization has already been accepted")

// ActivationStore is the PostgreSQL store for keys and transitions.
type ActivationStore struct {
	pool *pgxpool.Pool
}

func NewActivationStore(pool *pgxpool.Pool) *ActivationStore {
	return &ActivationStore{pool: pool}
}

func (s *ActivationStore) withTenant(ctx context.Context, tenantID string,
	fn func(pgx.Tx) error) error {

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

// RegisterKey records a key that may authorize activations.
func (s *ActivationStore) RegisterKey(ctx context.Context, k ActivationKey) error {
	if len(k.PublicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("an activation key must be a 32-byte ed25519 public key")
	}
	return s.withTenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO policy_activation_keys
				(tenant_id, key_id, algorithm, public_key, holder, status, valid_from, valid_until)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, key_id) DO UPDATE SET
				public_key  = EXCLUDED.public_key,
				holder      = EXCLUDED.holder,
				status      = EXCLUDED.status,
				valid_from  = EXCLUDED.valid_from,
				valid_until = EXCLUDED.valid_until`,
			k.TenantID, k.KeyID, AlgorithmEd25519, []byte(k.PublicKey), k.Holder,
			nonEmpty(k.Status, "ACTIVE"), k.ValidFrom.UTC(), k.ValidUntil)
		return err
	})
}

// RevokeKey stops a key authorizing anything further.
func (s *ActivationStore) RevokeKey(ctx context.Context, tenantID, keyID, by string,
	at time.Time) error {

	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE policy_activation_keys
			   SET status = 'REVOKED', revoked_at = $3, revoked_by = $4
			 WHERE tenant_id = $1 AND key_id = $2`,
			tenantID, keyID, at.UTC(), by)
		return err
	})
}

// Key returns one activation key.
func (s *ActivationStore) Key(ctx context.Context, tenantID, keyID string) (*ActivationKey, error) {
	var k ActivationKey
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var public []byte
		var revokedBy *string
		row := tx.QueryRow(ctx, `
			SELECT tenant_id, key_id, algorithm, public_key, holder, status,
			       valid_from, valid_until, revoked_at, revoked_by
			  FROM policy_activation_keys
			 WHERE tenant_id = $1 AND key_id = $2`, tenantID, keyID)
		if err := row.Scan(&k.TenantID, &k.KeyID, &k.Algorithm, &public, &k.Holder,
			&k.Status, &k.ValidFrom, &k.ValidUntil, &k.RevokedAt, &revokedBy); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return activationErr("ACTIVATION_KEY_UNKNOWN",
					"no activation key %s is registered for this tenant; a policy change "+
						"must be authorized by a key the customer registered", keyID)
			}
			return err
		}
		k.PublicKey = ed25519.PublicKey(public)
		if revokedBy != nil {
			k.RevokedBy = *revokedBy
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// Current returns the transition in force.
func (s *ActivationStore) Current(ctx context.Context, tenantID string) (*Transition, error) {
	var t Transition
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var prior, reason *string
		row := tx.QueryRow(ctx, `
			SELECT tenant_id, nonce, bundle_id, bundle_content_hash, prior_bundle_id,
			       action, actor, reason, key_id, authorized_at, accepted_at
			  FROM policy_activations
			 WHERE tenant_id = $1
			 ORDER BY accepted_at DESC
			 LIMIT 1`, tenantID)
		if err := row.Scan(&t.TenantID, &t.Nonce, &t.BundleID, &t.BundleContentHash,
			&prior, &t.Action, &t.Actor, &reason, &t.KeyID, &t.AuthorizedAt,
			&t.AcceptedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoTransition
			}
			return err
		}
		if prior != nil {
			t.PriorBundleID = *prior
		}
		if reason != nil {
			t.Reason = *reason
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// Accept records a transition and its evidence in one transaction.
//
// Both or neither. The caller switches enforcement only when this returns nil, so the
// platform cannot be enforcing a policy whose authorization went unrecorded — which is
// exactly what happened when evidence was appended after the switch and its error
// discarded.
func (s *ActivationStore) Accept(ctx context.Context, a Authorization, b *Bundle,
	prior *Transition, event evidence.Event, at time.Time) (*Transition, error) {

	t := Transition{
		TenantID:          a.TenantID,
		Nonce:             a.Nonce,
		BundleID:          a.BundleID,
		BundleContentHash: a.BundleContentHash,
		Action:            a.Action,
		Actor:             a.Actor,
		Reason:            a.Reason,
		KeyID:             a.Signature.KeyID,
		AuthorizedAt:      a.AuthorizedAt.UTC(),
		AcceptedAt:        at.UTC(),
	}
	if prior != nil {
		t.PriorBundleID = prior.BundleID
	}

	priorHash := ""
	if prior != nil {
		priorHash = prior.BundleContentHash
	}

	err := s.withTenant(ctx, a.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO policy_activations
				(tenant_id, nonce, bundle_id, bundle_content_hash, prior_bundle_id,
				 prior_bundle_content_hash, action, actor, reason, key_id,
				 authorized_at, accepted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (tenant_id, nonce) DO NOTHING`,
			t.TenantID, t.Nonce, t.BundleID, t.BundleContentHash,
			nullIfEmpty(t.PriorBundleID), nullIfEmpty(priorHash),
			string(t.Action), t.Actor, nullIfEmpty(t.Reason), t.KeyID,
			t.AuthorizedAt, t.AcceptedAt)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// The nonce is already recorded. A replay, and refusing it here means every
			// replica refuses it identically rather than each deciding for itself.
			return ErrReplayed
		}

		// Two different serialisations, deliberately. evidence_events stores the
		// payload column; the outbox carries the whole event, because the publisher
		// unmarshals a row back into an Event and puts it on the bus.
		//
		// They were the same value here, so every activation event was queued as a bare
		// payload, failed validation on "schema_version: required", and stayed in the
		// queue forever. The events were recorded correctly and none of them ever
		// reached the analytical plane.
		payload, err := json.Marshal(event.Payload)
		if err != nil {
			return err
		}
		queued, err := json.Marshal(event)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO evidence_events
				(event_id, schema_version, event_name, tenant_id, aggregate_id,
				 correlation_id, causation_id, occurred_at, produced_at, producer,
				 sequence, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (event_id, occurred_at) DO NOTHING`,
			event.EventID, event.SchemaVersion, string(event.EventName), event.TenantID,
			event.AggregateID, event.CorrelationID, nullIfEmpty(event.CausationID),
			event.OccurredAt.UTC(), event.ProducedAt.UTC(), event.Producer,
			event.Sequence, payload)
		if err != nil {
			return err
		}

		// The outbox row, in the same transaction, so the analytical plane learns about
		// the change through the same commit that made it.
		_, err = tx.Exec(ctx, `
			INSERT INTO evidence_outbox (tenant_id, event_id, subject, payload)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (event_id) DO NOTHING`,
			event.TenantID, event.EventID, event.Subject(), queued)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
