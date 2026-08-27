# ADR-015 — Idempotency truth lives in PostgreSQL; Redis is a cache

**Status:** ACCEPTED (resolves a contradiction in MASTER_BUILD_SPEC.md v0.1)

## Context

Three parts of the master spec collide:

- §17 requires that an exact duplicate request "Return deterministic prior outcome".
- §33.3 places the idempotency cache in Redis and states Redis is never a source of truth.
- INV-011 states that Redis loss cannot destroy authoritative financial-control state.

The prior outcome of an executable intent — the decision, and whether a broker order
was actually submitted — is authoritative financial-control state. If it exists only
in Redis, a Redis restart makes §17 unsatisfiable and violates INV-011. The spec never
resolves this, and §19 asks for "bounded retention" without naming a bound.

## Decision

1. Every executable envelope writes an idempotency record to PostgreSQL in the same
   transaction that records the authorization decision. The record holds at minimum:
   `tenant_id`, `idempotency_key`, `envelope_id`, `decision`, `broker_order_ref`,
   `outcome_hash`, `created_at`.
2. Redis holds a read-through cache of that record. A Redis hit is a latency
   optimization, never an authority.
3. On Redis miss or Redis unavailability, the gateway reads PostgreSQL. Enforcement
   continues; only latency degrades.
4. On PostgreSQL unavailability the gateway DENIES executable intents. This is
   consistent with §17 ("Hard policy unavailable -> DENY") because PostgreSQL holds
   authority grants, the active policy bundle, and idempotency truth.
5. Retention bounds, as documented defaults rather than spec truth:
   - PostgreSQL idempotency records: **90 days**, chosen to cover the incident
     reconstruction window in §49. Tunable via `IDEMPOTENCY_RETENTION_DAYS`.
   - Redis cache TTL: **24 hours**. Tunable via `IDEMPOTENCY_CACHE_TTL_SECONDS`.
   A duplicate arriving after retention expiry is treated as a new envelope and is
   subject to full authority and policy evaluation. It is never silently allowed.

## Consequences

- The hot path gains one PostgreSQL write on the decision transaction. This is
  measured against the §50.1 targets (p95 < 5 ms) in Phase 5; if it does not fit,
  the fix is batching or a local WAL, not moving truth to Redis.
- Phase 5 (broker lifecycle) and Phase 6 (evidence) depend on this record shape.
- The retention numbers are calibration knobs, not derived constants. They must be
  reviewed against real incident timelines before Phase 15.

## Prohibited reinterpretations

- Redis MUST NOT be the only store of a duplicate outcome, under any latency argument.
- A cache miss MUST NOT be treated as "no prior request".
- Retention expiry MUST NOT be used as a reason to skip authority evaluation.
