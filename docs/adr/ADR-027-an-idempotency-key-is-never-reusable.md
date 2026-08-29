# ADR-027: An idempotency key is never reusable

**Status:** Accepted
**Date:** 2026-08-29
**Phase:** V0
**Amends:** ADR-015

## Context

Two parts of the platform described the same key differently.

`internal/execution/retention.go` said that pruning a resolved idempotency record
**reopens its key**: a caller presenting it afterwards gets a fresh execution rather than
the earlier outcome, and the reuse guard cannot see a record that is gone. Thirty days
was described as "how long the platform can still recognise a retry".

`authority_usage` disagreed. Its rows are keyed by `(tenant, idempotency_key)` and are
never pruned, so a key presented again — at any age, under any envelope — is refused with
`RESERVATION_KEY_REUSED`. That is not a bug; it is the fix for B-003, where a reservation
left behind by a request that never reached a venue could authorize a different envelope,
a different grant and a different amount.

So the system said both "this key is fresh again" and "this key is permanently spent",
and which one a caller experienced depended on which layer answered first. Two lifecycles
with different meanings of "fresh" is not a window to tune; it is a contract nobody can
rely on.

## Decision

**An idempotency key identifies one economic request, permanently.**

- Retention prunes the *record* — the cached outcome and the ability to replay it — and
  does not reopen the *key*.
- `authority_usage` is the tombstone. It is not pruned, and a key it holds is refused for
  any request whose identity differs from the one it was taken for.
- After retention, a caller presenting an old key gets a refusal rather than a fresh
  execution. It is not a replay either: the outcome it would have replayed is gone.

The alternative — reuse permitted after a window — was rejected. It requires the
authority ledger to forget a key at the same moment the execution store does, in every
replica, with no ordering between the two sweeps. Any gap in either direction is a defect:
forgetting authority first lets a stale key authorize an amount nobody evaluated;
forgetting execution first is what this ADR describes. And the failure it would enable —
a duplicate execution under a key the platform no longer recognises — is precisely the one
INV-004 exists to prevent.

## Consequences

- **What retention buys is storage, not key freshness.** The window bounds the
  idempotency table; it does not bound what the platform remembers about a key.
- **A caller must not reuse keys.** They are cheap: a UUID per request costs nothing, and
  the platform refuses anything else. This is a documented API contract rather than a
  discovered behaviour.
- **The tombstone grows.** One row per economic request, permanently, in
  `authority_usage`. At V0 volumes this is small; compacting old rows to identity alone —
  dropping the notional and window fields once no window can include them — is a
  next-phase item and is not required for the contract to hold.
- **ADR-015 is amended.** Idempotency truth remains in PostgreSQL and the record is still
  bounded; the sentence about a pruned key being reopened no longer describes the system.
- **The prose changes with the code.** `internal/execution/retention.go` said the wrong
  thing for a release and no test could catch it, because it was a comment. The behaviour
  it now describes has a test that presents a key after its record is gone.
