# ADR-008 — Event delivery is at-least-once

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Exactly-once delivery across process and network boundaries is not free, and designing
as if it were produces silent double-counting under retry.

## Decision
Event consumers MUST be idempotent. Exactly-once distributed semantics are not a
design assumption.

## Consequences
- Every consumer needs a deduplication key and a defined replay behavior.
- Phase 6 exit criteria require that at-least-once duplication does not corrupt state.
- Schema consumers must tolerate unknown properties (packages/README.md item 5).

## Prohibited reinterpretations
- A broker-level exactly-once feature does not remove the obligation on the consumer.
- Deduplicating in the producer is not a substitute for an idempotent consumer.
