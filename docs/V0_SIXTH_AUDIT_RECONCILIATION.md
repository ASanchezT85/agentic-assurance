# V0 SIXTH AUDIT — RECONCILIATION AND ADVERSARIAL PROBES

**Build audited:** `ceea1a2` · **Build after remediation:** `e0d7788` · **Date:** 2026-08-30
**Method:** deliberately not the previous five.

The fifth review said its findings were the ones that pass happened to look for. So this
one changed the method rather than repeating it. Nothing here started from reading code I
had written. It started from three questions asked of the running system:

1. **Do the stores agree with each other?** Reconcile PostgreSQL against the outbox against
   ClickHouse, and look for orphans across every pair of tables that should line up.
2. **What does the platform say about itself?** It writes evidence about its own failures.
   Read it.
3. **What happens when someone attacks the boundaries from outside?** Cross-tenant reads,
   header claims, privilege probes against the live binaries.

Question 2 found the defect.

---

## STATUS

```text
A-6-01  HIGH   the release path had never worked; the evidence chain had been
               reporting it for months, and five audits never asked
```

One finding, fixed, with a structural guard that would have caught it and a red proof that
the guard works. Everything else examined held, and §5 lists what was attacked.

---

## A-6-01 — CAPACITY WAS NEVER RETURNED, AND THE PLATFORM SAID SO

**Severity: HIGH. INV-002, customer-visible over time.**

### The finding

`PostgresUsage.Release` returns capacity for an order that was never sent, by deleting the
reservation row — deliberately, so the key is genuinely free for a later, properly
evaluated request rather than leaving a stale row somebody could inherit.

`assurance_app` was never granted `DELETE` on `authority_usage`.

```text
=> SET app.tenant_id='tenant_probe_R';
=> DELETE FROM authority_usage WHERE ... AND state = 'RESERVED';
ERROR:  permission denied for table authority_usage
```

So every release since the table existed failed. Capacity reserved for a request that was
refused before the venue — an envelope reused, a key reused, a retired key, a venue that
definitively rejected — stayed consumed. A customer's rolling-hour and daily windows fill
with orders that do not exist, and legitimate orders are eventually refused against a limit
nobody spent. It is INV-002 enforced against the wrong number, and it gets worse with use.

### How it was found, which is the point

Not by reading the code. By asking the platform what it had already written down:

```sql
SELECT count(*) FILTER (WHERE payload ? 'reservation_not_released') AS failed_releases,
       count(*) FILTER (WHERE event_name='authority.reservation.released.v1') AS released_ok
  FROM evidence_events;

 failed_releases | released_ok
-----------------+-------------
              56 |           0
```

Fifty-six failures, zero successes, each carrying
`ERROR: permission denied for table authority_usage (SQLSTATE 42501)`.

The pipeline was written to record exactly this: when `Release` returns an error it emits an
event saying the reservation was not released, precisely so an operator can see it happened.
It worked. Nobody read it — through the third, fourth and fifth passes, all of which had
this database in front of them.

### Why nothing caught it

- The unit tests use `MemoryUsage`, whose `Release` is a map delete and always succeeds.
- **No integration test called `Release` at all.** `grep` for it across `tests/` returned
  nothing.
- The migrations grant privileges one table at a time; the code issues statements one file
  at a time; the two lists had never been compared.

### The fix

`0032_authority_usage_release.sql`: `GRANT DELETE ON authority_usage TO assurance_app`.
DELETE only, still under row level security — this returns capacity, it does not let
anything rewrite what was spent.

### Tests

```text
TestCapacityIsReturnedWhenNothingWasSent            PASS (was RED: permission denied)
  reserve 4,000 of 10,000, release, then reserve 9,000 — which only fits if the
  first reservation really went back

TestARefusedSubmissionReturnsItsCapacity            PASS
  end to end: a reused envelope is refused before the venue, and the chain carries
  authority.reservation.released.v1 rather than reservation_not_released

TestTheApplicationRoleMayRunTheStatementsItIssues   PASS (verified red)
  reads every INSERT / UPDATE / DELETE / FROM out of internal/ and asks the database
  whether assurance_app may run it. Revoking the new grant makes it fail naming
  "DELETE on authority_usage (internal\authority\usage.go)"
```

The third test is the one that matters for next time: it compares the two lists nobody had
compared, mechanically, so a statement added without its privilege fails in the suite rather
than in production.

---

## RECONCILIATION

### Across stores, on live data

31,001 events for one tenant after a 1,000-agent run:

```text
PostgreSQL evidence_events    31,001
ClickHouse evidence_stream    31,001
outbox unpublished                 0
```

Including `authority.grant.issued.v1` (1,000) and `agent.signing_key.registered.v1` — the
A-5-01 fix working end to end on the live system, which is what that finding was about.

### Orphans, across every tenant in the database

```text
reservations stuck RESERVED > 1h        1,237   test fixtures (tenant_bench, tenant_idem)
idempotency PENDING > 1h                1,227   the same fixtures
outbox unpublished > 1h                     0
tombstone with a live record                0   the two must never coexist
policy_current with no transition           0   (1 was this audit's own probe row)
activation keys with no grant            1,379  keys provisioned before the endpoint existed
```

Nothing pathological. The last line is expected and worth stating: `live-setup`, migrations
and tests write keys directly through the store, so `policy_activation_key_grants` is a
record of what the *endpoint* granted, not a complete provenance of every key. The bootstrap
rule accounts for this — it closes on any existing key, however it arrived.

---

## ADVERSARIAL PROBES

Run against the live binaries with two provisioned tenants.

| Probe | Result |
|---|---|
| B reads A's intent by id | `404 no such intent` |
| B reads A's evidence chain by correlation id | `200`, empty — existence not leaked |
| B reads A's intent evidence | `200`, empty |
| B presents `X-Tenant-Id: A` with its own token (gateway) | `403 the request names a tenant this caller is not authenticated for` |
| B presents `X-Tenant-Id: A` (fleet engine) | `401` — its credential registry is separate |
| fleet engine with no credential | `401` |
| `assurance_app` reads another tenant's tombstones / policy_current / key grants | 0 rows |
| `assurance_app` inserts a row for another tenant | `new row violates row-level security policy` |
| `assurance_app` updates or deletes a tombstone | `permission denied` |

Privileges on the ten tables that matter, read out of the catalog rather than the
migrations: `evidence_events` is `INSERT, SELECT` — append-only enforced by the grant, not
by convention — and so are `idempotency_tombstones`, `policy_activations` and
`policy_activation_key_grants`.

---

## WHAT WAS ATTACKED AND HELD

- **`PublishBatch` with four workers.** 300,000 events, zero errored rows, zero unpublished.
  A message neither acknowledged nor failed is left unpublished rather than marked
  delivered, so the failure mode is a retry.
- **The retired-key path cannot free spent capacity.** `Release` only deletes rows in
  `RESERVED`; a settled row is untouched. Confirmed against live data: 3,000 reservations,
  all `COMMITTED`, all with a resolved record, none orphaned.
- **`MarkPublishedBatch` honours the timestamp it is given.**
- The full matrix, after the fix: gate, integration, race over integration, chaos, process.

---

## A TEST DEFECT THIS PASS ALSO FOUND

Several tests I wrote in the fourth remediation set `app.tenant_id` on a **pool** and then
queried on the same pool. `set_config` applies to the connection that call happened to take;
the next query may take another, read through row level security with no tenant, and return
zero rows — which looks exactly like the rows having been deleted. One of them failed that
way here and sent me looking for a deletion that had not happened.

They run in one transaction now, through a helper that does what the stores already do
correctly. It is worth naming because the failure mode is a *passing* test in the other
direction: a count that reads zero where zero is the expected answer proves nothing.

---

## OPEN RISKS

Carried forward, plus what this pass added:

1. **The connection budget is undocumented.** The gateway, the fleet engine and a test run
   together reached 92 of PostgreSQL's 100 connections on this host, and the suite began
   failing with "failed to connect". Nothing states how many connections a deployable
   opens or what a deployment should size for.
2. **`Release` still has no caller-visible failure mode.** The pipeline records an event and
   carries on, which is right — the capacity stays held, which errs toward refusing later
   orders. But nothing alerts on it, and this finding is what that silence costs.
3. Retention is per tenant from the credential registry; an offboarded tenant is never swept.
4. Retention runs only in-process.
5. `authority_usage` grows without bound.
6. No endpoint lists registered keys.
7. The console has no human reviewer.
8. The capacity envelope is one host.

---

## WHAT THIS PASS IS WORTH

More than the fifth, for one reason: the method was different, so the findings were not the
ones I already knew to look for. A-6-01 had been visible in the platform's own evidence
since the reservation table existed, and it survived three audits and two self-reviews
because everyone — including the auditor — read the code and the tests, and nobody read the
record.

That generalises past this defect. This platform's product is a record of what happened;
the strongest audit of it is to read that record and check it against what the platform
claims. It is now one query, and it belongs in whatever runs against a live deployment:

```sql
SELECT count(*) FROM evidence_events WHERE payload ? 'reservation_not_released';
```

**V0 remains not self-accepted.** External audit of `e0d7788` is still what would settle it.
