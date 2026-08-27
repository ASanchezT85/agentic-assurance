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

## Broker timeout / unknown state

Written in Phase 5, because the failure mode now exists.

An order in `UNKNOWN` means the platform does not know whether the venue acted. It is
not a failed order and it is not a successful one.

**What the platform already did.** It submitted once, the response was lost or the
venue returned a 5xx, and it asked the venue once by our client order id. The answer
was inconclusive: either the venue could not be reached, or it reported no such order,
which does not establish that none was created.

**What the platform will not do.** Resubmit. There is no retry knob to turn, no
maximum-attempts setting, and no resubmit method: the capability is absent rather than
disabled, so no configuration change can produce a duplicate (INV-004).

**Operator steps.**

1. Find the record: `client_order_id` is `coid_<idempotency key>`, and the record is
   in `idempotency_records` with `state = 'PENDING'`.
2. Ask the venue directly, outside the platform, using that same client order id.
3. If the order exists, resolve the record to the state the venue reports.
4. If the venue confirms no order exists, and enough time has passed that a late
   arrival is implausible, resolve the record to the outcome the venue confirms.
5. Never resolve a record to a state you have not confirmed. A guessed resolution is
   worse than a pending one, because it stops anyone looking.

A pending record blocks further submissions on that idempotency key, which is the
intended behaviour: the agent's retry is safe precisely because it does nothing.
