# SECOND AUDIT REMEDIATION

**Build:** `004646f` · **Date:** 2026-08-29 · **Environment:** Windows 11 workstation,
Docker (PostgreSQL 16, ClickHouse 25.3, NATS 2, MinIO), Go 1.25.

## STATUS

Every item in the handoff was implemented and exercised. Nine defects were closed, four
of them found by the tests the audit asked for rather than by the audit itself. Nothing
here is a self-acceptance: the acceptance request at the end asks for a re-audit.

---

## B-003 RESERVATION IDENTITY

**Pre-fix reproduction.** A reservation was keyed by `(tenant, idempotency_key)` and
nothing else. A key left behind by a request that never reached a venue returned ALLOW
for a different envelope, a different grant and a different amount. Reproduced in four
shapes: an orphan key inherited by a new intent, a key crossing grants, a key surviving
the retention window, and the losing side of an envelope race.

**Implementation.** `authority.ReservationIdentity{EnvelopeID, PrincipalID, AccountID}`
is stored with the reservation (migration 0022) and compared on every repeat. A retry is
the same key *and* the same identity; anything else is refused with
`RESERVATION_KEY_REUSED`. `Release` returns capacity on definite non-submission so an
orphan is not left behind at all.

**Post-fix proof.** `tests/integration/reservation_identity_test.go` (T-B003-A/B/C/D),
and across two processes in `TestAReservationIsNotInheritableAcrossReplicas`, refused
with `IDEMPOTENCY_KEY_REUSED`.

---

## B-004 AUTHORITY CAPABILITIES

**Decision.** Removed from the executable contract. `margin_allowed` and
`shorting_allowed` were in the API, in the grant record and in two columns, and were read
by no check. A customer issuing a grant with `shorting_allowed: false` reads a control,
sizes their exposure against it, and the platform authorizes the short — deciding whether
a SELL is a short needs positions the platform does not hold. A denial that never comes
looks exactly like an order that never breached the rule, so the disagreement is
invisible from outside.

**Implementation / ADR.** ADR-026. The type and `Grant.Capabilities` are gone;
`POST /v1/authority-grants` refuses either field **by name** rather than dropping it
quietly, because a client relying on it must find out today. Migration 0024 keeps the
columns — past grants record what a customer asked for — under a `CHECK` holding them at
false. The spec's grant example carries the divergence note.

**Proof.** `TestAGrantMayNotCarryAPermissionNothingEnforces` (both `true` and `false`
refused), and `TestAuthorityCarriesNoUnenforcedPermission`, which fails if the field
returns to the grant record.

---

## B-005 EXACT MONEY

**Representation.** `internal/money`. `Amount` is an `int64` of ten-thousandths — scale
4, the database's scale. Parsed from decimal text; exponents and excess precision are
**refused**, not rounded, because silently rounding an input turns a caller's order into
a different order. No `Mul` or `Div`: the one place a rounding rule is needed is
`Notional(price, quantity)`, which rounds **up**, away from zero, so a ceiling counts at
least what an order can cost.

**Migration.** 0023 widens `authority_usage.notional` from `numeric(20,2)` to
`numeric(20,4)`. Limits were already scale 4, so the number a ceiling was evaluated
against was not the number counted against it.

**Boundary/property tests.** `internal/money/money_test.go` (0.1+0.2, a thousand
fractional additions, refusal of excess precision, JSON round trip, a property test over
200×5000 accumulations). And §13 below, which found that the reservation was not
enforcing the per-order ceiling at all.

---

## B-006 ARCHIVE CONTENT INTEGRITY

**Canonical event hash.** `retention.ContentHash` covers the whole record including the
payload, length-prefixed, with the payload canonicalized by the same algorithm that signs
an envelope. The previous hash covered only identity fields, so an archived authority
decision could be edited from `{"allowed": true}` to `{"allowed": false}` and the chain
still verified.

**Tamper tests.** `TestEvidenceArchivesAndRestores/a_tampered_payload_is_refused` makes
exactly that edit against a real object store and the chain refuses it — by the chain,
not by the count: the archive is the same length as the manifest says.

**Object-store test.** See RETENTION EXPORT/RESTORE.

---

## R-010 EXPIRED STATE

**Settlement.** EXPIRED is terminal: the notional stays spent, the open-order count is
released.

**Event.** `broker.order.expired.v1`, no longer a `default` branch answering "accepted".
An unmapped state now becomes `broker.order.unknown.v1` — a state an operator resolves,
not a claim about a venue.

**Tests.** `tests/security/state_catalog_test.go` walks `internal/broker` and fails on a
state with no decided meaning; `internal/gateway/state_mapping_test.go` asserts what the
switches actually do, including the unknown-state default. The fake broker gained
`FaultExpire`, because EXPIRED was a canonical state no test could reach.

---

## R-011 SUBMISSION EVIDENCE

**New event semantics.** The receipt recorded `broker.order.submitted.v1` *before* the
broker was called, so an audit export, a regulator's reconstruction or an incident
timeline read an intention as a submission — and the two differ in exactly the cases that
matter.

- `execution.decision.committed.v1` — the receipt. Every check passed, capacity is held.
- `broker.order.submission_attempted.v1` — after the call. Not recorded for a refused
  claim, a replay, or a reconciliation: an attempt that stopped inside the platform was
  not an attempt on a venue.
- The outcome stays what the venue said; an ambiguous timeout still means nobody knows.

**Tests.** `TestEvidenceDoesNotClaimAnUnsentOrder` (receipt fails; reused envelope), and
the chain assertions in the unit and integration E2E tests.

---

## R-012 RESERVATION EVIDENCE

**Events.** `authority.reserved.v1` with the amount held,
`authority.reservation.committed.v1` and `authority.reservation.released.v1`. Each
settlement event is written **after** the write it describes: an event saying capacity was
returned, emitted ahead of the write that returns it, is a claim rather than a record.

**Tests.** `TestAReservationIsEvidence`, and §15's producer test, which collects them
from a real run.

---

## R-013 POLICY ACTIVATION AUDIT

**Customer control.** Unchanged and deliberate: the gateway verifies a signature and
reads what the customer staged. It does not activate policy (INV-009, ADR-010).

**Actor attribution.** `policy.bundle.activated.v1` carries the bundle id, content hash,
rule count and the actor the customer's own activation names. The gateway is never the
actor; an activation that named nobody is written as empty rather than filled in with the
process that noticed.

**Rollback evidence.** `policy.bundle.rolled_back.v1`, decided by whether this gateway has
had the bundle in force before — not by a version number, because a customer restoring the
previous bundle republishes the same version. Per process, so the first activation after a
restart reads as an activation; that limit is written where the rule is.

**Tests.** `TestActivationAndRollbackAreRecorded`: two activations and one rollback, with
the actor asserted, and an unchanged file read twice producing nothing.

---

## R-014 OUTBOX DB ROLE

**Roles.** `assurance_outbox`, `LOGIN NOSUPERUSER NOBYPASSRLS`, with `SELECT, UPDATE` on
`evidence_outbox` and `SELECT` on `evidence_events`. It can mark what it drained and
cannot create, delete or alter an event.

**RLS.** Migration 0025 replaces one `FOR ALL USING (true)` policy granted to
`assurance_app` with two: the application sees the tenant its transaction set, the
publisher sees all of them. The old grant was written for a background job and landed on
the role every request handler connects as. The gateway builds the publisher from
`POSTGRES_OUTBOX_DSN` and without it starts no publisher, because a publisher on the
application role sees an empty queue and a drained outbox is indistinguishable from a
stalled one.

**Tests.** `TestOnlyThePublisherRoleReadsAcrossTenants`, asserted where it is enforced:
the publisher sees two tenants' rows, the application sees neither.

---

## MULTI-REPLICA RESULT

Two gateways, separate connection pools, one database, one grant.

```
ceiling            10,000 rolling
attempts           6 x 4,000 = 24,000, alternating between replicas
allowed            2
spent              8,000
ledger             8,000.0000  (exact match)
```

`TestTwoReplicasCannotOverspendOneGrant`. The B-003 exploit run across replicas is
refused. The single-process test could not prove this: two goroutines in one process may
be held apart by a mutex or the scheduler, and a ceiling that holds only inside one
process is not a ceiling for a deployment that runs three.

---

## CRASH-RECOVERY RESULT

Reconstructed rather than mocked: the claim commits, the venue accepts, nothing resolves
it — the state a `kill -9` between the two leaves — and a second process picks up the key.

```
submissions at the venue     1 before, 1 after
record after recovery        RESOLVED, outcome ACCEPTED (reconciled)
reservation                  settled; open orders 1, notional 1,000 of 10,000
evidence                     no submission_attempted from the recovering process
```

**This test found a defect.** The recovering process recorded
`broker.order.submission_attempted.v1` while it had attempted nothing — it asked the venue
what had already happened. A reconciled outcome is fresh and not-replayed, so it looked
exactly like an attempt. `Outcome.Reconciled` now says how an outcome was obtained: the
R-011 defect had a second home in the recovery path.

---

## CURRENT-BUILD PERFORMANCE

Against the running gateway binary. No figure is compared with a pre-remediation run.

**1,000 agents × 3 signed intents**

```
3,000 submissions in 11.0s      272/s
statuses    200:3000            codes  ACCEPTED:3000
end-to-end over HTTP            p50 3.610s  p95 3.798s  p99 3.896s
```

The end-to-end figure is dominated by client-side queueing: the harness caps at 256
sockets because a thousand simultaneous connections overflow the listen backlog on this
workstation.

**Sustained, two minutes**

```
minute 0    15,449 decided    p50 189ms  p95 236ms  p99 277ms   200:15449
minute 1    14,288 decided    p50 196ms  p95 274ms  p99 437ms   200:14288
total       29,787 decisions  248/s sustained
```

**Multi-tenant** — two tenants, 40 intents each, neither listing the other's.

**Per stage, against real PostgreSQL**

```
signature verification   p50 76µs     p95 106µs    p99 107µs   (batched; see note)
authority reservation    p50 5.47ms   p95 7.00ms   p99 7.95ms
evidence receipt         p50 9.79ms   p95 12.09ms  p99 13.50ms  (6 events + outbox rows)
idempotency claim        p50 3.79ms   p95 5.44ms   p99 6.48ms
```

Signature verification is timed in batches of 200 and divided: one verification is below
this platform's clock resolution, and per-call timing reported a p50 of exactly zero — a
figure produced by the clock rather than by the code.

**The load harness now signs.** Envelopes are verified since remediation, so an unsigned
run would have measured the latency of a refusal.

---

## CURRENT-BUILD ALPACA PAPER

```
TestAlpacaPaperAcceptsAndReconcilesAnOrder   PASS
  accepted    broker_order_id=62ad0af4-c440-4488-afca-ec54676be7a5  state=ACCEPTED
  reconciled  state=ACCEPTED  filled=0
TestAlpacaPaperRefusesAnUnmappedInstrument   PASS
```

Paper endpoint only. Both adapters refuse a non-paper endpoint; no real-money path exists
in V0.

---

## LIVE BINARY E2E

`scripts/live-boot.sh` + `cmd/live-setup` provision a tenant, grant, agent keys, a signed
ACTIVE bundle and an instrument map through the same packages the platform uses, then
build and start `assurance-gateway` and `fleet-engine`.

```
gateway         listening, "submission path served: POST /v1/intents"
fleet-engine    listening, "consuming evidence  durable=fleet-evidence  subject=evidence.>"
submissions     3,000 signed intents over HTTP, all ACCEPTED
outbox          drained to 0
projection      29,999 events for the live tenant in assurance.evidence_stream
```

**The first boot proved the point of the item.** `BROKER=fake` without
`ASSURANCE_ENV=development` leaves "submission path not served" — correct behaviour, and
invisible from any test that constructs a Pipeline in process.

---

## RETENTION EXPORT/RESTORE

`PostgreSQL period → JSON Lines → S3-compatible bucket → manifest (count + chain head) →
verify → restore`. Against MinIO in `docker-compose.yml`.

```
successful archive            PASS   25 events, chain head recorded
successful restore            PASS   payloads intact
payload tamper rejected       PASS   refused by the chain, not by the count
truncation rejected           PASS   refused by the count; remaining hashes are all valid
wrong manifest rejected       PASS   both a foreign chain head and a wrong count
legal hold prevents destroy   PASS   permitted before, refused under hold, permitted after release
failed upload leaves source   PASS   no manifest written, 25 events still in place
```

Nothing archives on a schedule and nothing is ever destroyed. `Destroy` is the gate a
future purge must pass, not a purge with a flag. A hold lookup that *fails* is also a
refusal: unknown is not "none".

Written against the S3 protocol rather than a vendor SDK — the obvious client brought
thirteen transitive dependencies and the repository's allowlist guard refused them. This
is one PUT and one GET off the hot path. The platform does not create the bucket: where a
customer's evidence is archived, and under what object-lock settings, is their decision.

---

## FULL TEST MATRIX

**PASS**

```
quality gate (gofmt, vet, lint, unit, guards, console build)   PASS
go test ./...                                                  PASS
go test -tags=integration ./tests/integration/...              PASS   32s
race detector (scripts/test-race.sh, in a container)           PASS
1,000-agent load (live binary)                                 PASS
sustained load, 2 minutes (live binary)                        PASS
multi-tenant isolation under load (live binary)                PASS
per-stage latency (signature, reservation, receipt, claim)     PASS
two-replica grant ceiling                                      PASS
cross-replica reservation identity                             PASS
crash after venue acceptance                                   PASS
retention export/restore, 7 cases, MinIO                       PASS
authority precision property                                   PASS
state catalog + state mapping completeness                     PASS
event producer completeness                                    PASS
Alpaca Paper (tags=live)                                       PASS
chaos suite (make test-chaos), 8 cases                         PASS
SPIRE / mTLS submission, 10 cases                              PASS
```

The chaos suite stops real containers and runs alone: enforcement survives outages of
ClickHouse, NATS, Redis and the intelligence plane; PostgreSQL fails **closed**; a missing
policy bundle denies; a broker timeout does not duplicate; a gateway restart loses nothing
that matters.

**FAIL** — none outstanding.

**SKIPPED** — none. Both items skipped in the first draft of this matrix were run
afterwards and are recorded above.

**NOT RUN**

```
A3 attestation                    never produced in V0, by design
destructive purge                 disabled by decision; the gate is tested, the deletion does not exist
production-representative load    every figure here is a laptop talking to containers on the same laptop
```

---

## MIGRATIONS

```
0022_reservation_identity.sql          envelope/principal/account on authority_usage
0023_usage_scale.sql                   authority_usage.notional numeric(20,2) -> (20,4)
0024_capabilities_out_of_contract.sql  CHECK holding margin/shorting at false; columns kept
0025_outbox_publisher_role.sql         assurance_outbox role; split RLS policies
```

All four have `.down.sql`. 0024 and 0025 were **not repeatable** on first write — a replay
of the full set failed on an existing constraint and an existing policy. Both now drop
first. `scripts/migrate.sh` also creates the local MinIO bucket when MinIO is running.

---

## ADRs

```
ADR-026  Capabilities are not part of V0 authority
```

---

## FILES CHANGED

76 files, +4,784 / −259 across eleven commits:

```
9fe3def  EXPIRED recorded as accepted; archive hash ignored the payload
fdb6b4d  OpenAPI regenerated
3ed4d89  B-003 reservation identity
f31169a  B-005 exact money
4814562  R-011 + R-012 evidence semantics
5dd9286  B-004 capabilities removed (ADR-026)
2322bb4  R-014 publisher role
77f6ae8  R-013 policy activation evidence
3e5643b  §11 retention export/restore
11cd8a7  §12.1 + §12.2
004646f  §12.3/12.4/12.5, §13, §14, §15
```

---

## DEVIATIONS

1. **One signing key across the synthetic fleet.** A thousand agent identities with a
   thousand grants, one key pair. Generating a thousand pairs would measure key
   generation. Related gap below.
2. **The two replicas share one fake broker instance.** They share the database, which is
   where the ceiling is enforced; a shared in-process venue is closer to reality than two.
3. **Latency figures are from a Windows workstation** talking to containers on the same
   machine, with a ~0.5 ms clock granularity that required batching for the signature
   measurement.
4. **The archive is JSON Lines, uncompressed.** Compression is a size decision and would
   put a codec between the bytes that were hashed and the bytes that were stored.

---

## OPEN RISKS

1. **The outbox drains slower than the hot path fills it.** Measured this round: the
   sustained run produced ~2,200 events/s into `evidence_outbox` and the publisher drained
   ~100–200/s, leaving a queue of 131,346 that took roughly twenty minutes to clear after
   the load stopped. Nothing on the hot path waits for it and no evidence is lost, but the
   analytical plane and the fleet engine lag a busy period by tens of minutes. The cause is
   one JetStream publish plus one `UPDATE` per event; batching the marks is the obvious
   remedy. **Not fixed this round** — it is a capacity defect the acceptance runs revealed,
   not an item on the handoff.
2. **No endpoint registers an agent signing key.** Onboarding an agent today requires
   database access. `cmd/live-setup` does it directly, which is why the load harness could
   sign at all.
3. **Rollback detection is per process.** The first activation after a restart is recorded
   as an activation even when an operator considers it a rollback. The gateway did not
   witness the earlier activation, and that is written where the rule is.
4. **Retention has no scheduler.** Nothing archives automatically; the path exists and is
   proved, and running it is a manual act.
5. **`evidence_outbox` reads are cross-tenant for one role.** Narrowed from the whole
   application to a `SELECT/UPDATE`-only publisher, and it is still the one place in the
   system where a role sees every tenant.
6. **The console and fleet-engine were not re-audited** against these changes beyond the
   build and the integration suite.

---

## V0 ACCEPTANCE REQUEST

Requesting a third audit against `004646f`.

Not requesting acceptance. Four of the defects closed this round were found by the tests
the second audit asked for rather than by the audit itself — the per-order ceiling missing
from the reservation, the recovery path claiming a submission it never made, `authority.Save`
writing twenty-one placeholders into nineteen columns, and `MemoryStore` not enforcing the
envelope uniqueness PostgreSQL enforces. Three of those four were invisible until something
ran the code end to end with every dependency actually attached, and the fourth was
invisible until a property test generated a value nobody had thought to write down.

That is the argument against self-acceptance. The remaining defects of this build are, by
construction, the ones no test written so far is shaped to see.
