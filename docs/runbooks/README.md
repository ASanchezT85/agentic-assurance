# Runbooks

Spec §61 requires nine runbooks. Each is written by the phase that creates the failure
mode it describes; a runbook for a system that does not exist yet would be fiction.

| Runbook | Owning phase | Fail semantics |
|---|---|---|
| Broker timeout / unknown state | 5 | Reconcile-first. No blind retry (INV-004). |
| Policy rollback | 4 | Explicit and audited. Never edit in place (§43). |
| Intelligence cloud outage | 4 | Local hard enforcement continues (INV-005). |
| NATS outage | 6 | Buffer locally within bounds; hot path unaffected (ADR-021). |
| Redis outage | 5 | Fall through to PostgreSQL; latency only (ADR-015, INV-011). |
| ClickHouse outage | 8 | Analytics degrade; hot path unaffected (ADR-021). |
| Compromised workload identity | 2 | Revoke, then DENY on the next attempt (INV-001). |
| Emergency cohort halt | 13 | Requires explicit customer policy (INV-009). |
| Tenant incident investigation | 10 | Read the evidence chain (ADR-023). Never cross tenants. |

**PostgreSQL outage** is not in the §61 list but is the most consequential failure in
the system: executable intents are DENIED (ADR-021). Its runbook is written in Phase 5
alongside the idempotency store.
