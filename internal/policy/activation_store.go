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

// ErrStalePredecessor means the authorization describes a transition from a bundle that
// is not the one in force.
//
// The signed document says "move from this to that". Verifying only the second half makes
// every authorization valid for ever: one signed and set aside can be presented after the
// customer has moved somewhere else, and it silently undoes the decision that overtook it.
// It is also what let two replicas each accept a different transition from one predecessor
// and branch a tenant's history.
var ErrStalePredecessor = errors.New("this authorization describes a transition from a policy that is not the one in force")

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
//
// It reports whether it registered. An existing key is never replaced: overwriting the
// public key under a key id would let one request substitute the authority that decides
// which policy enforces, and every document signed by the previous key would stop
// verifying at the same moment. Rotation is a new key id, then a revocation.
//
// This used to upsert, and a caller who registered a key that was already taken was told
// nothing at all while somebody else's key stayed in force.
func (s *ActivationStore) RegisterKey(ctx context.Context, k ActivationKey) (bool, error) {
	if len(k.PublicKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("an activation key must be a 32-byte ed25519 public key")
	}
	registered := false
	err := s.withTenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO policy_activation_keys
				(tenant_id, key_id, algorithm, public_key, holder, status, valid_from, valid_until)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, key_id) DO NOTHING`,
			k.TenantID, k.KeyID, AlgorithmEd25519, []byte(k.PublicKey), k.Holder,
			nonEmpty(k.Status, "ACTIVE"), k.ValidFrom.UTC(), k.ValidUntil)
		registered = err == nil && tag.RowsAffected() == 1
		return err
	})
	return registered, err
}

// ActiveKeys counts the keys that could authorize something now.
//
// It decides whether a registration is a bootstrap or an extension of existing authority:
// a tenant with no key cannot sign for the first one.
func (s *ActivationStore) ActiveKeys(ctx context.Context, tenantID string) (int, error) {
	var count int
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM policy_activation_keys
			 WHERE tenant_id = $1 AND status = 'ACTIVE' AND revoked_at IS NULL`,
			tenantID).Scan(&count)
	})
	return count, err
}

// RegisterKeyAuthorized registers a key and records what authorized it, in one
// transaction, with its evidence.
//
// Both or neither, for the reason Accept gives: a key able to decide which policy
// enforces must not become usable through a commit that did not also record who granted
// it. The nonce is the primary key of the record, so a replayed authorization is refused
// by the database on every replica rather than by whichever process happened to remember.
//
// signedBy is empty for the bootstrap — the first key of a tenant, which nothing could
// have signed for — and the record says so.
func (s *ActivationStore) RegisterKeyAuthorized(ctx context.Context, k ActivationKey,
	nonce, action, actor, signedBy string, authorizedAt time.Time, event evidence.Event,
	at time.Time) (bool, error) {

	if len(k.PublicKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("an activation key must be a 32-byte ed25519 public key")
	}

	registered := false
	err := s.withTenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO policy_activation_key_grants
				(tenant_id, nonce, action, subject_key_id, actor, signed_by_key_id,
				 authorized_at, accepted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (tenant_id, nonce) DO NOTHING`,
			k.TenantID, nonce, action, k.KeyID, actor, nullIfEmpty(signedBy),
			authorizedAt.UTC(), at.UTC())
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrReplayed
		}

		tag, err = tx.Exec(ctx, `
			INSERT INTO policy_activation_keys
				(tenant_id, key_id, algorithm, public_key, holder, status, valid_from, valid_until)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (tenant_id, key_id) DO NOTHING`,
			k.TenantID, k.KeyID, AlgorithmEd25519, []byte(k.PublicKey), k.Holder,
			nonEmpty(k.Status, "ACTIVE"), k.ValidFrom.UTC(), k.ValidUntil)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// The key id is taken. Not registered, and the grant record must not stand
			// either: rolling back leaves the nonce unused, so the same authorization
			// can be presented again once the conflict is resolved.
			return errKeyExists
		}
		registered = true

		return appendEventTx(ctx, tx, event)
	})
	if errors.Is(err, errKeyExists) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return registered, nil
}

// errKeyExists is internal: it rolls the transaction back and is reported to the caller
// as "not registered" rather than as a failure.
var errKeyExists = errors.New("the key id is already registered")

// RevokeKey stops a key authorizing anything further.
//
// It refuses the last active key. A tenant with no usable activation key can never
// authorize another policy change — not a rollback during an incident, not a new key —
// and recovering from that needs database access, which is the state this whole endpoint
// exists to remove. Register the replacement first, then revoke.
//
// Revocation itself is deliberately not signed for. Containment has to be fast, and the
// case that matters is a key believed compromised: requiring that key's own cooperation
// to retire it would be requiring the attacker's cooperation.
func (s *ActivationStore) RevokeKey(ctx context.Context, tenantID, keyID, by string,
	at time.Time) (bool, error) {

	revoked := false
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var active int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM policy_activation_keys
			 WHERE tenant_id = $1 AND status = 'ACTIVE' AND revoked_at IS NULL`,
			tenantID).Scan(&active); err != nil {
			return err
		}
		if active <= 1 {
			return activationErr("ACTIVATION_KEY_LAST",
				"key %s is the only active activation key for this tenant. Revoking it "+
					"would leave nobody able to authorize a policy change, including the "+
					"rollback an incident needs. Register the replacement first.", keyID)
		}

		tag, err := tx.Exec(ctx, `
			UPDATE policy_activation_keys
			   SET status = 'REVOKED', revoked_at = $3, revoked_by = $4
			 WHERE tenant_id = $1 AND key_id = $2 AND status = 'ACTIVE'`,
			tenantID, keyID, at.UTC(), by)
		revoked = err == nil && tag.RowsAffected() == 1
		return err
	})
	return revoked, err
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
//
// Read from policy_current, which is the pointer a transition updates under its own lock.
// It used to be "the row with the greatest accepted_at", and accepted_at is a timestamp the
// accepting gateway generated: a replica two hours behind made its own change look older
// than the one it replaced, so what a restart read as current depended on whose clock was
// wrong.
func (s *ActivationStore) Current(ctx context.Context, tenantID string) (*Transition, error) {
	var t Transition
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var nonce string
		if err := tx.QueryRow(ctx,
			`SELECT nonce FROM policy_current WHERE tenant_id = $1`, tenantID).Scan(&nonce); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoTransition
			}
			return err
		}
		loaded, err := transitionInTx(ctx, tx, tenantID, nonce)
		if err != nil {
			return err
		}
		t = *loaded
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// transitionInTx reads one accepted transition by its nonce.
func transitionInTx(ctx context.Context, tx pgx.Tx, tenantID, nonce string) (*Transition, error) {
	var t Transition
	var prior, reason *string
	row := tx.QueryRow(ctx, `
		SELECT tenant_id, nonce, bundle_id, bundle_content_hash, prior_bundle_id,
		       action, actor, reason, key_id, authorized_at, accepted_at
		  FROM policy_activations
		 WHERE tenant_id = $1 AND nonce = $2`, tenantID, nonce)
	if err := row.Scan(&t.TenantID, &t.Nonce, &t.BundleID, &t.BundleContentHash,
		&prior, &t.Action, &t.Actor, &reason, &t.KeyID, &t.AuthorizedAt,
		&t.AcceptedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoTransition
		}
		return nil, err
	}
	if prior != nil {
		t.PriorBundleID = *prior
	}
	if reason != nil {
		t.Reason = *reason
	}
	return &t, nil
}

// Accept records a transition and its evidence in one transaction, if and only if the
// predecessor the customer signed is the one in force.
//
// Everything happens under the tenant's row in policy_current, so this is the point where a
// tenant's policy history is serialized. Three things had to move inside that lock:
//
//   - the predecessor check. The signed document says "move from this to that"; only the
//     second half was verified, so an authorization signed and set aside stayed valid for
//     ever and could undo whatever had happened since;
//
//   - the ordering. transition_seq is assigned here rather than read from accepted_at,
//     which is a timestamp the accepting gateway generated;
//
//   - the current pointer. Two replicas could each accept a different transition from one
//     predecessor because nothing they both had to hold said what that predecessor was.
//
// Both or neither, as before: the caller switches enforcement only when this returns nil,
// so the platform cannot enforce a policy whose authorization went unrecorded.
func (s *ActivationStore) Accept(ctx context.Context, a Authorization, b *Bundle,
	event evidence.Event, at time.Time) (*Transition, error) {

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

	err := s.withTenant(ctx, a.TenantID, func(tx pgx.Tx) error {
		// The serialization point. An advisory lock rather than SELECT FOR UPDATE alone,
		// because the first transition of a tenant has no row to lock and that is exactly
		// when two concurrent first activations would both insert.
		if _, err := tx.Exec(ctx,
			"SELECT pg_advisory_xact_lock(hashtext($1))", "policy_current:"+a.TenantID); err != nil {
			return err
		}

		var (
			currentNonce string
			currentID    string
			currentHash  string
			seq          int64
		)
		err := tx.QueryRow(ctx, `
			SELECT nonce, bundle_id, bundle_content_hash, transition_seq
			  FROM policy_current WHERE tenant_id = $1 FOR UPDATE`,
			a.TenantID).Scan(&currentNonce, &currentID, &currentHash, &seq)
		hasCurrent := true
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			hasCurrent = false
		case err != nil:
			return err
		}

		if err := checkPredecessor(a, hasCurrent, currentID, currentHash); err != nil {
			return err
		}
		if hasCurrent {
			t.PriorBundleID = currentID
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO policy_activations
				(tenant_id, nonce, bundle_id, bundle_content_hash, prior_bundle_id,
				 prior_bundle_content_hash, action, actor, reason, key_id,
				 authorized_at, accepted_at, transition_seq)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			ON CONFLICT (tenant_id, nonce) DO NOTHING`,
			t.TenantID, t.Nonce, t.BundleID, t.BundleContentHash,
			nullIfEmpty(t.PriorBundleID), nullIfEmpty(currentHash),
			string(t.Action), t.Actor, nullIfEmpty(t.Reason), t.KeyID,
			t.AuthorizedAt, t.AcceptedAt, seq+1)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// The nonce is already recorded. A replay, and refusing it here means every
			// replica refuses it identically rather than each deciding for itself.
			return ErrReplayed
		}

		if err := appendEventTx(ctx, tx, event); err != nil {
			return err
		}

		// The pointer moves last and in the same commit. Enforcement becomes eligible
		// only after this transaction commits.
		_, err = tx.Exec(ctx, `
			INSERT INTO policy_current
				(tenant_id, nonce, bundle_id, bundle_content_hash, transition_seq, accepted_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (tenant_id) DO UPDATE SET
				nonce               = EXCLUDED.nonce,
				bundle_id           = EXCLUDED.bundle_id,
				bundle_content_hash = EXCLUDED.bundle_content_hash,
				transition_seq      = EXCLUDED.transition_seq,
				accepted_at         = EXCLUDED.accepted_at`,
			t.TenantID, t.Nonce, t.BundleID, t.BundleContentHash, seq+1, t.AcceptedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// checkPredecessor compares what the customer signed against what is in force.
//
// Both the id and the content hash, for both actions. The id is a name a customer chooses
// and can reuse; the hash is what the rules actually are, so an authorization binding only
// the name authorizes whatever later took it.
func checkPredecessor(a Authorization, hasCurrent bool, currentID, currentHash string) error {
	if !hasCurrent {
		// The tenant's first transition. Naming a predecessor it never had describes a
		// history that did not happen.
		if a.PriorBundleID != "" || a.PriorBundleContentHash != "" {
			return activationErr("ACTIVATION_STALE_PREDECESSOR",
				"the authorization names %s as the policy in force and this tenant has "+
					"none: %w", a.PriorBundleID, ErrStalePredecessor)
		}
		return nil
	}

	if a.PriorBundleID == "" || a.PriorBundleContentHash == "" {
		return activationErr("ACTIVATION_PREDECESSOR_MISSING",
			"this tenant is enforcing %s and the authorization names no predecessor. A "+
				"policy change is a transition from one state to another, and one that "+
				"does not say what it is replacing cannot be checked against what it is "+
				"actually replacing: %w", currentID, ErrStalePredecessor)
	}
	if a.PriorBundleID != currentID || a.PriorBundleContentHash != currentHash {
		return activationErr("ACTIVATION_STALE_PREDECESSOR",
			"the authorization moves from %s (%s) and this tenant is enforcing %s (%s). "+
				"An authorization that was overtaken cannot undo the decision that "+
				"overtook it: %w",
			a.PriorBundleID, short(a.PriorBundleContentHash), currentID, short(currentHash),
			ErrStalePredecessor)
	}
	return nil
}

// appendEventTx writes one evidence event and its outbox row inside a caller's
// transaction.
//
// Two different serialisations, deliberately. evidence_events stores the payload column;
// the outbox carries the whole event, because the publisher unmarshals a row back into an
// Event and puts it on the bus.
//
// They were the same value once, so every activation event was queued as a bare payload,
// failed validation on "schema_version: required", and stayed in the queue forever. The
// events were recorded correctly and none of them ever reached the analytical plane.
func appendEventTx(ctx context.Context, tx pgx.Tx, event evidence.Event) error {
	payload, err := json.Marshal(event.Payload)
	if err != nil {
		return err
	}
	queued, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO evidence_events
			(event_id, schema_version, event_name, tenant_id, aggregate_id,
			 correlation_id, causation_id, occurred_at, produced_at, producer,
			 sequence, payload)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (event_id, occurred_at) DO NOTHING`,
		event.EventID, event.SchemaVersion, string(event.EventName), event.TenantID,
		event.AggregateID, event.CorrelationID, nullIfEmpty(event.CausationID),
		event.OccurredAt.UTC(), event.ProducedAt.UTC(), event.Producer,
		event.Sequence, payload); err != nil {
		return err
	}

	// The outbox row, in the same transaction, so the analytical plane learns about the
	// change through the same commit that made it.
	_, err = tx.Exec(ctx, `
		INSERT INTO evidence_outbox (tenant_id, event_id, subject, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_id) DO NOTHING`,
		event.TenantID, event.EventID, event.Subject(), queued)
	return err
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
