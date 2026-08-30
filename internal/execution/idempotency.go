// Package execution owns the order lifecycle: idempotency, submission, and
// reconciliation of ambiguous outcomes.
//
// The rule the whole package exists to enforce is spec section 19 and INV-004: an
// ambiguous broker timeout must never produce a second submission. Everything here
// is arranged so that resubmitting is not a decision anyone can accidentally make.
package execution

import (
	"context"
	"errors"
	"time"

	"agentic-assurance/internal/broker"
)

// ErrEnvelopeReused means a second intent arrived carrying an envelope id that has
// already been submitted under a different idempotency key.
//
// Spec section 12.2 makes an envelope id identify one intent. Two idempotency keys
// under one envelope id is a caller that generated a fresh key for a request it also
// called a repeat of an earlier one, and honouring both produces two orders for one
// stated intention. Refused rather than reconciled: the platform cannot tell which of
// the two the caller meant.
// ErrKeyReused is returned when an idempotency key is presented with a different
// envelope than the one that claimed it.
//
// The mirror of ErrEnvelopeReused, and the more dangerous direction. That one is two
// keys for one stated intention; this is two intentions under one key, and until it was
// checked the second caller was handed the first one's outcome — so an agent that
// reused a key for a different order was told its order had been accepted and filled
// when nothing of the kind had been sent. An assurance platform answering for an order
// that does not exist is the failure it exists to prevent.
var ErrKeyReused = errors.New("this idempotency key was claimed by a different envelope")

var ErrEnvelopeReused = errors.New("this envelope id was already submitted under a different idempotency key")

// ErrKeyRetired means the key belonged to a request whose record retention has pruned.
//
// Distinct from ErrKeyReused, which is a live record claimed by another envelope. This is
// the state ADR-027 describes and the fourth audit found unenforced: the outcome was
// deliberately deleted, so there is nothing to replay, and the request it identified was
// completed once. Executing it again would be a second venue submission for one economic
// request (INV-004); replaying it would mean inventing an outcome the platform threw away.
// The only honest answer is a refusal that says the key is spent.
var ErrKeyRetired = errors.New("this idempotency key belongs to a request that has already been completed and whose outcome has been pruned")

// RecordState is the lifecycle of an idempotency record.
type RecordState string

const (
	// RecordPending means a submission was claimed and its outcome is not yet
	// known. A pending record found on a later request means the previous attempt
	// died mid-flight, and the answer is to reconcile, never to submit again.
	RecordPending RecordState = "PENDING"

	// RecordResolved means the outcome is final for this idempotency key.
	RecordResolved RecordState = "RESOLVED"
)

// Outcome is the deterministic result returned for an idempotency key.
//
// Spec section 17 requires an exact duplicate to return the prior outcome, so this
// is what gets stored and replayed rather than recomputed.
type Outcome struct {
	State          broker.ExecutionState
	ClientOrderID  string
	BrokerOrderID  string
	FilledQuantity float64
	RejectReason   string

	// Replayed marks an outcome that was returned from the record rather than
	// produced by a call to the venue. The caller needs to know, and the audit
	// trail needs to say so.
	Replayed bool

	// Reconciled marks an outcome the platform learned by asking the venue what
	// happened, rather than by sending anything.
	//
	// It exists because the two are indistinguishable from the outside and must not be
	// from the inside: a process recovering a PENDING record after a crash produces a
	// fresh, non-replayed outcome without making a single submission, and evidence that
	// cannot tell the difference records an attempt that nobody made.
	//
	// Never persisted, like Replayed. How an outcome was obtained is a property of the
	// call that obtained it, not of the outcome.
	Reconciled bool
}

// Record is the authoritative idempotency record.
//
// ADR-015: this lives in PostgreSQL, written in the same transaction as the
// authorization decision. Redis holds a copy and is never consulted as the truth.
type Record struct {
	TenantID       string
	IdempotencyKey string
	EnvelopeID     string
	ClientOrderID  string
	State          RecordState
	Outcome        Outcome
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ErrRecordNotFound is returned by a store when nothing matches.
var ErrRecordNotFound = errors.New("idempotency record not found")

// Store is the authoritative idempotency repository.
type Store interface {
	// Claim atomically inserts a PENDING record, or reports the one already there.
	//
	// claimed is true only when this caller created the record. Everyone else gets
	// existing, and must not submit.
	Claim(ctx context.Context, rec Record) (existing *Record, claimed bool, err error)

	// Resolve writes the final outcome for a key.
	Resolve(ctx context.Context, tenantID, idempotencyKey string, o Outcome, at time.Time) error

	// Load reads a record.
	Load(ctx context.Context, tenantID, idempotencyKey string) (*Record, error)
}

// Cache is an optional read-through copy of resolved records.
//
// It is deliberately a smaller interface than Store: there is no Claim, because a
// cache must never be able to decide that a submission is new. Losing every entry
// costs latency and nothing else (INV-011).
type Cache interface {
	Get(ctx context.Context, tenantID, idempotencyKey string) (*Record, bool)
	Put(ctx context.Context, rec Record)
}
