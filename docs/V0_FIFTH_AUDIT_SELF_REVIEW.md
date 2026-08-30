# V0 FIFTH AUDIT — SELF-REVIEW OF THE FOURTH REMEDIATION

**Build audited:** `4f847e3` · **Build after remediation:** `d2095a1` · **Date:** 2026-08-30
**Auditor:** the implementer. This is a self-review, and §6 says what that is worth.

No fifth-audit handoff exists. This was run in its place, against the work the fourth
remediation had just declared complete, looking for what that pass broke or left rather
than for confirmation that it worked.

---

## STATUS

Three findings, all confirmed by running rather than by reading, all fixed and measured:

```text
A-5-01  CRITICAL   administrative evidence never reached the bus
A-5-02  HIGH       the outbox had no retention: 4.3 GB of an 8.3 GB database
A-5-03  MEDIUM     the fourth remediation put a second round trip on every claim
```

A-5-01 contradicts a line in the fourth report's own matrix. That is the finding worth the
most here, and §6 is about why a self-review found it at all.

---

## A-5-01 — ADMINISTRATIVE EVIDENCE NEVER REACHED THE BUS

**Severity: CRITICAL. Analytical completeness, INV-005 adjacent.**

### What was wrong

`evidence.Store` has two write paths. `AppendBatch` writes the events and their outbox rows
in one transaction. `Append` — one event at a time — wrote the event and nothing else.

The hot path batches, so a decision's six events reached NATS and ClickHouse. Everything
written one at a time did not:

```text
authority.grant.issued.v1          POST /v1/authority-grants
authority.grant.revoked.v1         POST /v1/authority-grants/{id}/revoke
fleet.control.applied.v1           POST /v1/controls
fleet.control.revoked.v1           POST /v1/controls/{id}/revoke
agent.signing_key.registered.v1    POST /v1/agent-keys
agent.signing_key.revoked.v1       POST /v1/agent-keys/revoke
policy.activation_key.revoked.v1   POST /v1/policy-activation-keys/revoke
incident.opened.v1                 the fleet engine's detector
simulation.*                       the Digital Twin
```

That is the administrative half of the record — every act an operator performs — and it is
exactly the half an incident review goes looking for. The enforcement plane's own account
in PostgreSQL was complete; the copy the analytical plane holds was missing all of it.

### How it was found

On the running gateway, not in a test. Issue a grant over HTTP, then ask the database
whether the event it wrote is owed to the bus:

```text
issue grant: 201
        event_name         | in_outbox
---------------------------+-----------
 authority.grant.issued.v1 | f
```

The fourth report's matrix says `producer → outbox → NATS → consumer → projection  PASS`.
It was true of the path that test exercised — the batched one — and I read it as true of
the platform.

### The fix

`Append` enqueues in the same transaction, exactly as `AppendBatch` does, and only when the
insert actually recorded something, so appending the same event twice is not a second
delivery. A new `AppendConsumed` writes without enqueueing, for the consumer path: an event
that arrived from the bus must not be queued back onto it, which would be a loop.

### Tests

```text
TestEveryRecordedEventIsQueuedForTheBus   PASS   (red: the live query above)
TestAConsumedEventIsNotQueuedBack         PASS
```

---

## A-5-02 — THE OUTBOX HAD NO RETENTION

**Severity: HIGH. Unbounded growth in the enforcement plane's own database.**

### What was wrong

A delivered outbox row was never removed. Every event the platform has ever produced stayed
in `evidence_outbox` after publication, carrying the whole event as its `payload` — a second
complete copy of the evidence stream, growing without limit.

Measured on the reference workstation after this pass's load runs:

```text
evidence_outbox        4293 MB
published rows        4,045,425
database               8283 MB
```

More than half the database was delivery receipts for messages that had already been
delivered. Idempotency records have retention; evidence is partitioned by month and has an
archive path; the queue in front of the bus had nothing, and nothing in the system would
ever have removed a row.

### The fix

`Store.PrunePublished(tenant, before, batch)` and an `OutboxSweeper` in the gateway, beside
the idempotency sweeper and for the same reason — the process that owns the table is the
process that should bound it.

```text
OUTBOX_RETENTION_HOURS=24     a receipt outlives the incident that would ask about it
OUTBOX_SWEEP_MINUTES=60
```

An unpublished row is never pruned at any age. That is work the platform still owes the bus,
and this is retention, not a way to forget an obligation.

### Test

```text
TestDeliveredOutboxRowsArePruned   PASS
  two rows delivered 48 h ago and two still owed
  -> both delivered rows deleted, both owed rows still queued
```

---

## A-5-03 — THE TOMBSTONE CHECK COST A ROUND TRIP ON EVERY CLAIM

**Severity: MEDIUM. Hot-path latency, self-inflicted by F4-B001.**

The permanence fix added a `SELECT` against `idempotency_tombstones` before the insert, on
every claim — including the overwhelming majority where nothing is retired. The idempotency
claim is the largest single item in the §50.1 budget, so a second round trip there is not
free:

```text
before F4-B001      p50 3.34 ms     (third report, smaller database)
after  F4-B001      p50 4.94 ms  p95 6.89 ms  p99 8.20 ms
after  this fix     p50 4.05 ms  p95 5.29 ms  p99 6.96 ms
```

The check is now a `WHERE NOT EXISTS` on the insert itself, so the common case costs one
statement as it always did; the tombstone is read only when nothing was inserted and no live
record explains why. The remaining gap to 3.34 ms is measured against a database that has
since grown to 8 GB, and the two numbers are not directly comparable.

---

## WHAT WAS CHECKED AND FOUND SOUND

Not everything examined was broken. These were specifically attacked and held:

- **`PublishBatch` with several workers.** `PublishAsyncComplete` is per-connection, so a
  worker could in principle observe another's completion. Run at `OUTBOX_WORKERS=4` for two
  minutes: 300,000 events, **zero** rows with an error, zero unpublished, 2,492/s. And a
  message that is neither acknowledged nor failed is left unpublished rather than marked
  delivered, so the failure mode is a retry, not a loss.
- **`MarkFailed` does not lose a row.** It increments the attempt count and records the
  reason; `published_at` stays null and the row is claimed again.
- **The policy CAS under the process harness**, twice more. One transition, both processes
  agreeing, the loser enforcing nothing the database refused.
- **The retention transaction**, with the tombstone insert privilege revoked mid-flight:
  retention fails and the record survives.

---

## FULL RE-RUN, AFTER THE FIXES

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| integration, full suite | **PASS** |
| race over unit + security + integration | **PASS** |
| chaos, 9 scenarios, alone | **PASS** |
| process, real binaries | **PASS** |
| outbox sustained capacity, 2 min, 1 worker | **PASS** (2,493/s in, 2,488/s out, catch-up 203 ms) |
| outbox sustained capacity, 4 workers | **PASS** (zero errors, zero unpublished) |
| hot-path latency | **PASS** (claim p50 4.05 ms) |
| live binary boot + key smoke, 8 checks | **PASS** |
| Alpaca Paper | **PASS** (run before these fixes; untouched by them) |

---

## OPEN RISKS

Carried forward, plus what this review added:

1. **Retention is per tenant, from the credential registry.** Both sweepers iterate the
   tenants the gateway is configured for, so rows belonging to a tenant no longer served are
   never swept — including the four million rows this workstation accumulated under test
   tenants. It is the honest consequence of having no other list of who exists, and it means
   an offboarded tenant leaves data behind until somebody removes it deliberately.
2. **Retention still has no scheduler outside the gateway.** Both sweepers run in-process;
   a deployment that never starts a gateway never bounds anything.
3. **`authority_usage` grows without bound**, now genuinely redundant with the tombstone for
   the identity question.
4. **No endpoint lists registered keys.** Rotation is done blind.
5. **The console has no human reviewer.**
6. **The capacity envelope is one host.**

---

## WHAT THIS SELF-REVIEW IS WORTH

A-5-01 exists because the fourth remediation's matrix line — producer to projection, PASS —
was true of the test that produced it and not of the platform. I wrote both the test and the
line. The reason this review caught it is that it went looking at the running system for
something the tests do not assert, rather than re-reading the tests; the reason the fourth
pass did not is that I checked the path I had just built.

That is the argument against treating this document as an audit. It is a second pass by the
same person, and its findings are the ones that pass happened to look for. The three fixes
here are real and measured; the confidence they should buy is confidence in these three
things, not in the build.

**V0 remains not self-accepted.** An external fifth audit of build `d2095a1` is still the
thing that would settle it, and the most useful place for it to start is the list in §6 of
the fourth report — the questions I could not answer about my own work.
