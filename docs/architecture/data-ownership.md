# Data ownership

Three stores, three jobs. Confusing them is how financial control state gets lost.

## PostgreSQL — source of truth

Authoritative control-plane state. Everything here survives a total loss of every
other store.

Tenants, users, principals, accounts, agents, workload identities, attestations,
authority grants, authority revocations, policy bundles, policy deployment records,
instruments, broker connections, incidents, incident actions, control actions,
simulation definitions, scenario metadata, experiment metadata.

Plus, per ADR-015: **idempotency records**. The prior outcome of an executable intent
is authoritative financial-control state, so it lives here, written in the same
transaction as the decision.

Isolation: row level security, tenant context in the application layer, tenant-scoped
credentials (§34). Cross-tenant queries are prohibited in the normal application path.

## ClickHouse — analytical telemetry

Intents, broker orders, fills, fleet measurements, rolling windows, dependency
observations, cohort observations, anomaly features, simulation telemetry.

ClickHouse is **prohibited from the synchronous hard-policy path** (§59). Losing it
degrades analytics and nothing else (ADR-021).

## Redis — ephemeral only

Rolling hot state, idempotency **cache**, counters, bounded buffering, rate-limiting
state.

Redis is never the canonical source of authority, policy, incident history, execution
truth, or a prior idempotent outcome. A cache miss is not evidence that a request is
new (ADR-015). Losing Redis costs latency, never correctness (INV-011).

The development container runs with persistence disabled on purpose, so that nobody
can quietly start depending on it surviving a restart.

## Decision table

| Question | Store |
|---|---|
| Does this agent have authority right now? | PostgreSQL |
| Which policy bundle decided this order? | PostgreSQL |
| Have I already executed this idempotency key? | PostgreSQL, cached in Redis |
| What happened, in order, for this correlation id? | PostgreSQL (evidence), exposed by ADR-023 |
| What is the model concentration in this cohort this hour? | ClickHouse |
| How many intents per second is this instrument seeing? | Redis for the live counter, ClickHouse for the baseline |
| What did the simulator produce for seed 42? | PostgreSQL for the experiment record, ClickHouse for telemetry |

## Retention

| Data | Bound | Authority |
|---|---|---|
| Idempotency records (PostgreSQL) | 90 days | ADR-021 |
| Idempotency cache (Redis) | 24 hours TTL | ADR-021 |
| Local telemetry buffer | 512 MB or 1 hour, whichever first | ADR-021 |
| Evidence | Append-only; never selectively edited | ADR-009 |

The first three are calibration knobs sized for a development host. They must be
re-derived from measured throughput before Phase 15.

## Measured, not assumed (Phase 8)

Ingest into ClickHouse was measured at **~228,000 intents/sec**: 50,000 rows through
four workers in batches of 5,000, over the HTTP interface, against local Docker.

That number needs its qualifier every time it is quoted. It measures ClickHouse
ingest of pre-built envelopes and nothing else. The 10,000/sec target in spec section
50.3 is about the platform sustaining intents, which also costs envelope validation,
identity verification, authority evaluation, policy evaluation and a broker call.
**Ingest being fast means ingest is not the bottleneck. It does not mean the platform
sustains this rate**, and the end-to-end figure needs the full path, which is not
wired yet.

The test prints this caveat alongside the number, so the two cannot be separated by
someone reading test output.

## Why measurements are stored rather than recomputed

`fleet_measurements` holds computed results, not just inputs. A historical view built
by re-running today's code over yesterday's rows shows what the current logic would
have concluded, which is not what happened. An incident review needs the second, and
storing the measurement is the only way to have it.
