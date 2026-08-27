# ADR-021 — Completed fail-semantics table and named bounds

**Status:** ACCEPTED (completes MASTER_BUILD_SPEC.md §17 and §19)

## Context

ADR-005 requires every subsystem to declare fail-open, fail-closed, fail-static or
reconcile-first behavior, and states there is no global fail-open mode. The §17 table
has no row for PostgreSQL unavailable and no row for NATS unavailable. It also says
telemetry must "buffer locally within configured bounds" without naming a bound, and
§19 requires "bounded retention" without naming one. An unnamed bound is chosen at
implementation time by whoever is closest to the keyboard, which is precisely the
improvisation ADR-005 exists to prevent.

## Decision

### Added rows to the §17 table

| Condition | Required behavior |
|---|---|
| PostgreSQL unavailable | **DENY** executable intents (fail-closed). It holds authority grants, the active policy bundle and idempotency truth (ADR-015). |
| NATS unavailable | **Continue local hard enforcement.** Buffer telemetry to local disk within the bounds below. Never block the hot path. |
| ClickHouse unavailable | **No hot-path effect.** ClickHouse is prohibited from the synchronous policy path (§59); analytics degrade only. |
| Local telemetry buffer full | Drop **oldest** buffered telemetry, increment a `telemetry_dropped_total` counter, emit an operational alert. Never drop a decision record — decisions live in PostgreSQL, not in the event stream. |

### Named bounds (documented defaults, not spec truth)

| Bound | Default | Env var |
|---|---|---|
| Local telemetry buffer size | 512 MB | `TELEMETRY_BUFFER_MAX_BYTES` |
| Local telemetry buffer age | 1 hour | `TELEMETRY_BUFFER_MAX_AGE_SECONDS` |
| Idempotency retention (PostgreSQL) | 90 days | `IDEMPOTENCY_RETENTION_DAYS` |
| Idempotency cache TTL (Redis) | 24 hours | `IDEMPOTENCY_CACHE_TTL_SECONDS` |

Whichever telemetry bound is reached first triggers the drop-oldest behavior.

## Consequences

- The gateway can enforce with NATS, ClickHouse, Redis and the fleet engine all down.
  It cannot enforce with PostgreSQL down, and it fails closed rather than guessing.
- Chaos tests (§55) assert exactly this table. Each row gets a test in `tests/chaos/`.
- These numbers are calibration knobs sized for a single-tenant development host. They
  must be re-derived from measured throughput before Phase 15 signs off.

## Prohibited reinterpretations

- No subsystem may be given fail-open behavior not written in this table or §17.
- Dropping telemetry MUST NOT be silent. A dropped-events counter without an alert is
  the same as losing the data twice.
- A decision record MUST NOT be routed through the telemetry buffer.
