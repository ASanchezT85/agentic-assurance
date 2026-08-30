# V0 FOURTH AUDIT REMEDIATION

**Build:** `c507309` · **Date:** 2026-08-30 · **Environment:** Windows 11 workstation,
Docker (PostgreSQL 16, ClickHouse 25.3, NATS 2, MinIO, SPIRE), Go 1.25, fakebroker and
Alpaca Paper.

**Scope:** `V0_FOURTH_AUDIT_REMEDIATION_HANDOFF.md` in full, plus
`V0_FOURTH_AUDIT_REMEDIATION_ADDENDUM_KEY_REGISTRATION.md`.

---

## STATUS

Four release blockers and six addendum findings were reproduced in red, fixed, and
re-measured. Two defects were found by the new tests rather than by the audit, and one by
the live smoke test after everything else was green; all three are described below rather
than folded into a pass.

**No new product surface was added in this pass.** The scope-control finding in §1 of the
addendum is accepted without argument: the key-registration endpoints were built after the
fourth audit had already named its blockers, and building them then was wrong regardless of
their merits. They exist, they have been audited, and nothing else was added.

V0 is **not** self-accepted. This requests a fifth audit.

---

## F4-B001 PERMANENT IDEMPOTENCY

### Pre-fix reproduction

`TestAPrunedRequestCannotReachTheVenueAgain`, over HTTP, PostgreSQL throughout, fakebroker
at the far end, asserting on `fakebroker.Submissions` rather than order count:

```text
submit K/E/G/P/A/1000.0000        200 ACCEPTED, Submissions(coid) == 1
retention prunes the resolved record
submit the identical signed request again
                                  200 ACCEPTED
                                  Submissions(coid) == 2      <-- RED
```

`TestAPrunedEnvelopeCannotExecuteUnderANewKey`: the same envelope under a new key reached
the venue after the prune, because the unique envelope index lives on the record retention
had just deleted. `TestTwoReplicasCannotResurrectARetiredRequest`: two replicas racing a
retired key produced two submissions.

The audit's diagnosis was exact. `PostgresUsage.Reserve` refuses a key whose *identity*
differs and allows the identical request through as a retry — right while the record exists,
and after the prune there was nothing left to refuse or replay.

### Architecture

Permanence lives in the idempotency domain, not in authority:

```text
idempotency_records      live, replayable, pruned by retention
      |  one transaction
      v
idempotency_tombstones   permanent identity: key, envelope, client order id, final state
```

`authority_usage` was rejected as the universal tombstone for the audit's reasons and one
more that a test proved: a quantity-sized market order under a grant that caps orders rather
than money has no reservation row at all, so the row that would remember the key does not
exist (`TestPermanenceDoesNotDependOnAMonetaryReservation`).

### Migration

`0029_idempotency_tombstones.sql`. `PRIMARY KEY (tenant_id, idempotency_key)`,
`UNIQUE (tenant_id, envelope_id)`, RLS and FORCE RLS, `GRANT SELECT, INSERT` only — no
UPDATE and no DELETE, because a tombstone that can be removed is a key that can be reopened.

`Prune` selects the batch `FOR UPDATE`, inserts the tombstones and deletes exactly those
rows in one transaction. There is no instant at which the outcome is gone and the identity
is forgotten.

### Claim semantics after retention

| Request | Answer |
|---|---|
| Same key | `IDEMPOTENCY_KEY_RETIRED` (422). Not executed, not replayed — the outcome was deliberately pruned |
| Same key, different envelope | Refused (`RESERVATION_KEY_REUSED` from authority, which sees it first) |
| Same envelope, new key | `ENVELOPE_REUSED` |
| New key, new envelope | Ordinary fresh request |

### Broker submission counts

| Test | Submissions |
|---|---|
| identical request after prune | **1** (was 2) |
| same envelope, new key, after prune | **0** |
| two replicas racing a retired key | **1** (was 2) |

### Tests

```text
F4-IDEMPOTENCY-01  TestAPrunedRequestCannotReachTheVenueAgain            PASS (was RED)
F4-IDEMPOTENCY-02  TestAPrunedKeyIsRetiredForADifferentEnvelope          PASS
F4-IDEMPOTENCY-03  TestAPrunedEnvelopeCannotExecuteUnderANewKey          PASS (was RED)
F4-IDEMPOTENCY-04  TestPermanenceDoesNotDependOnAMonetaryReservation     PASS
F4-IDEMPOTENCY-05  TestRetentionIsAtomic                                 PASS
F4-IDEMPOTENCY-06  TestTwoReplicasCannotResurrectARetiredRequest         PASS (was RED)
```

F4-IDEMPOTENCY-05 injects the failure by revoking `INSERT` on the tombstone table
mid-flight: retention fails, the record survives, and after the grant is restored the prune
completes and the key is retired.

PENDING records are still never pruned.

### ADR-027

Amended. It names `idempotency_tombstones` as the tombstone, and states the pre-broker
boundary explicitly: a request that has not crossed the durable execution claim may retry;
past it the identity is permanent, and a different economic request may never inherit the
key. Authority reservation deletion does not define idempotency semantics.

---

## F4-B002 POLICY TRANSITION CAS

### Pre-fix: stale authorization

`TestAStaleAuthorizationCannotReorderPolicyHistory`. B0 in force; A1 signed for B0→B1 and
set aside; the customer activates B0→B2 instead; A1 is then presented.

```text
before:  A1 accepted, tenant moved to B1        <-- RED, the customer's last decision undone
after:   ACTIVATION_STALE_PREDECESSOR
         "the authorization moves from bundle_f4_b0 (8f343fba35bc) and this tenant is
          enforcing bundle_f4_b2 (21bd2ba21b82)"
```

### Pre-fix: concurrent branch

`TestTwoConcurrentBranchesCannotBothCommit`. Two providers, one database, both authorized
from B0, started together: **both committed** before the fix. Now exactly one commits, one
transition row is added, and the loser writes no evidence.

### Current-pointer design

`0030_policy_current.sql`:

```text
policy_current
  tenant_id PRIMARY KEY
  nonce, bundle_id, bundle_content_hash
  transition_seq bigint      assigned under the lock, never read from a clock
  accepted_at
```

`policy_activations` stays append-only and gains `transition_seq`, backfilled by the old
ordering with a unique index on `(tenant_id, transition_seq)`; the pointer is populated for
tenants that already had a history.

`Accept` now runs entirely under `pg_advisory_xact_lock('policy_current:'||tenant)` plus
`SELECT … FROM policy_current … FOR UPDATE` — the advisory lock because a tenant's *first*
transition has no row to lock, which is exactly when two concurrent first activations both
inserted. Inside it: read the predecessor, compare, insert the transition with `seq+1`,
append evidence and outbox, move the pointer. Enforcement changes only after that commit.

### Predecessor verification

Both id and content hash, for both actions:

| Case | Rule |
|---|---|
| First transition | `prior_bundle_id` and `prior_bundle_content_hash` must be **empty** |
| ACTIVATE | both must equal what is in force |
| ROLLBACK | both must equal what is in force |
| Either, missing predecessor | `ACTIVATION_PREDECESSOR_MISSING` |

A rollback naming the right id and the wrong hash is refused
(`TestARollbackWithTheWrongPredecessorHashIsRefused`). The id is a name a customer chooses;
the hash is what the rules are.

### Database ordering and clock skew

`Current` reads `policy_current`. It used to be `ORDER BY accepted_at DESC` over a timestamp
the accepting gateway generated. `TestCurrentPolicyIsDeterministicUnderClockSkew` applies
the second transition with a clock two hours behind and asserts the pointer still names it.

### Multi-replica proof

`TestTwoGatewayProcessesCannotBranchPolicyHistory` (tag `process`): two real gateway
processes, separate bundle directories, separate pools, one submission through each at the
same moment.

```text
submissions answered 200 ACCEPTED and 403 POLICY_UNAVAILABLE
transitions for the tenant: 2 (the provisioned one, and one branch)
in force: bundle_left (3e77af8c55d8)
the loser answered 403 POLICY_UNAVAILABLE to an order its own bundle would allow
the winner answered 200 ACCEPTED
```

The loser fails closed rather than enforcing the bundle the database refused. A process
whose first reload was refused has no policy it is entitled to evaluate against, and
refusing is the honest answer; what it must never do is accept an order the in-force policy
denies, which is what the probe checks.

### Evidence result

The losing branch writes no `policy.bundle.activated` and no `policy.bundle.rolled_back`.
Asserted in `TestTwoConcurrentBranchesCannotBothCommit`.

### Tests

```text
F4-POLICY-01  stale unused ACTIVATE                      PASS (was RED)
F4-POLICY-02  ACTIVATE, prior id right, hash wrong       PASS
F4-POLICY-03  ROLLBACK, prior id right, hash wrong       PASS
F4-POLICY-04  two concurrent branches                    PASS (was RED: both committed)
F4-POLICY-05  two real gateway processes                 PASS
F4-POLICY-06  clock skew                                 PASS
F4-POLICY-07  restart reads the database pointer         PASS
F4-POLICY-08  losing branch emits no evidence            PASS
F4-POLICY-09  losing branch changes no enforcement       PASS
```

Existing fixtures now read what is in force and sign a transition from it, which is what a
customer's tooling does.

---

## F4-B003 JETSTREAM ACK DURABILITY

### Pre-fix: ACK before insert

`TestAFailedProjectionInsertLeavesMessagesRedeliverable`, real JetStream, injectable sink
failing its first call:

```text
before:  0 of 25 events were redelivered after the insert failed        <-- RED
         a restarted consumer recovered 0 of 10
after:   25 of 25 redelivered, 25 rows written, each event once
```

### Consumer API change

```go
func (c *Consumer) FetchBatch(ctx, n int, wait time.Duration, h BatchHandler) (int, error)
```

Decode the whole fetch, call the handler once, and only then acknowledge — NAK everything
on failure. `Fetch` is unchanged and still right for handlers that do their own durable
work; the projection was not one of those.

Malformed messages are terminated and **counted**, and the count is surfaced through
`Consumer.Report`, which `RunEvidenceConsumer` logs at error level with its consequence.
Discarding an event is a decision; one that says nothing is indistinguishable from a bug.

### Sink failure injection

`EvidenceProjection.Sink` is now `EvidenceBatchSink`, an interface with one method. Failure
is injected at the sink, not by mocking JetStream — a mock of the broker would test the mock.

### Redelivery and restart proof

```text
F4-PROJECTION-01  failure before first insert -> nothing acked          PASS (was RED)
F4-PROJECTION-02  sink recovers -> all redelivered and inserted         PASS
F4-PROJECTION-03  retried batch -> each event once                      PASS
F4-PROJECTION-04  consumer restart recovers the pending batch           PASS (was RED)
F4-PROJECTION-05  malformed message -> terminated and reported          PASS
F4-PROJECTION-06  successful insert -> ACK after the insert             PASS
```

---

## F4-B004 OUTBOX STEADY CAPACITY

### Previous test limitation

It appended 10,000 events, then created a publisher, then measured catch-up. Every finite
backlog clears once the producer stops. It also had no publisher during arrival, so the
result depended on whether an unrelated gateway happened to be running — with one, depth
stayed shallow; without one, the same code reached 100% of arrivals in flight.

### The new test

`tests/performance/outbox_capacity_test.go`, tag `load`, self-contained: it owns the
producer and the publishers, and refuses to run at all if anything else is draining the
database.

```text
canary event appended, left for 3 s
if it is published by something else -> TEST_ENVIRONMENT_CONTAMINATED, fail
```

| Parameter | Value | Why |
|---|---|---|
| Duration | 120 s | the handoff's minimum |
| Target arrival | 2,500 events/s | the build produces ~2,250: nine events per decision at ~250 decisions/s |
| Producers | 4 goroutines, 125-event batches | to reach the target |
| Workers | `OUTBOX_WORKERS`, default **1** | the supported single-gateway configuration |
| Sampling | every 2 s | depth, oldest unpublished age, produced, published |

### The first result: it failed

```text
produced 300000 events in 2m0s (2497/s); published 290169 (1207/s)
catch-up 2m0.19s; depth after 10000
  t=  2s depth=  2500 oldest=1.001s
  t= 20s depth= 26000 oldest=10.411s
  ... depth grew linearly for the whole run
```

One publisher served 1,207/s against 2,497/s arriving. The ceiling was one synchronous
round trip to NATS per event: `Publish` waits for the server's acknowledgement before
sending the next message.

### The fix

`Publisher.PublishBatch` sends a batch with `PublishAsync` and waits for **every**
acknowledgement before any row is marked published. What changed is that the round trips
overlap; what did not change is that nothing is marked published until the server has
acknowledged it. An event whose acknowledgement never arrives is reported failed, as before.

### The measurement

```text
produced 300000 events in 2m0s (2497/s)
published 300001            (2493/s)
mean depth: first half 198, second half 175      <-- not trending upward
oldest unpublished age: <= ~250 ms throughout
catch-up after the producer stopped: 202 ms
depth after: 0
evidence store: 300000 of 300000                 <-- no loss
workers: 1 (the default)
```

### Capacity envelope

A single gateway with `OUTBOX_WORKERS=1` sustains **≥ 2,490 evidence events per second** on
the reference host — above the ~2,250 this build produces at its measured decision rate.
The setting is explicit and documented in `.env.example` so a deployment that outgrows one
publisher raises it rather than relying on there happening to be several replicas.

### Other outbox tests

```text
F4-OUTBOX-01  2 min at target, bounded queue                    PASS
F4-OUTBOX-02  service rate measured explicitly                  PASS (2,493/s)
F4-OUTBOX-03  oldest age bounded                                PASS (<= ~250 ms)
F4-OUTBOX-04  publisher killed mid-batch, lease recovers        PASS
F4-OUTBOX-05  two workers, no overlapping claims                PASS
F4-OUTBOX-06  NATS outage and recovery                          PASS (500 queued, 119 ms)
F4-OUTBOX-07  no external publisher dependency                  PASS (guard added)
```

---

## ADDENDUM: KEY REGISTRATION

### F4-K001 bootstrap serialization

Two concurrent bootstraps both committed (`results: 201 act_race_0 / 201 act_race_1`).
Migration `0031` makes it a unique partial index — one `BOOTSTRAP_ACTIVATION_KEY` grant per
tenant, ever — and the decision moved inside the committing transaction.

**A further defect, found by the live smoke test after the unit and integration tests were
green.** "Has this tenant ever bootstrapped" was answered from the grant table alone, and
the tenant `live-setup` provisions has an activation key written straight through the
store, which records no grant. The API bootstrapped a second policy authority into a tenant
that already had one: 201 where the smoke test expected 400. The rule is now two conditions,
both checked in the transaction: never bootstrapped **and** holds no activation key however
it was provisioned. Keys-only would reopen the bootstrap when the last key was revoked;
grants-only ignores every other way a key can arrive.

```text
RED F4-K001   two concurrent bootstraps both succeed         reproduced, now 201 + 409
              bootstrap reopens after the only key expires    reproduced, now refused
```

### F4-K002 signed validity

`subject_valid_from` and `subject_valid_until` are now fields of `KeyAuthorization`, inside
the signed bytes. An authorized registration carries the signed document **and nothing
else**: any other field in the request is refused, rather than the platform choosing which
unsigned fields are harmless.

```text
sign valid_until = T1, present with wrapper valid_until = T1+365d
  -> 400 "an authorized registration carries the signed authorization and nothing else"
sign valid_until = T1, present alone
  -> 201, stored valid_until == T1
```

### F4-K003 signer revalidation

The authorizing key's row is re-read `FOR UPDATE` inside the registration transaction and
re-checked with `Usable(at)`. Signature verification still happens before the transaction,
for CPU; the authority decision happens inside it.

```text
sign with key-A, revoke key-A, then present
  -> 403 ACTIVATION_KEY_REVOKED     (was: registered anyway)
```

### F4-K004 last usable key

`ActiveKeys` became `UsableKeys`: status, revocation **and** the validity window, against
the database's clock. The revocation guard uses the same query.

```text
key-A usable, key-B ACTIVE but expired; revoke key-A
  -> 409 ACTIVATION_KEY_LAST_USABLE  (was: allowed, leaving zero usable keys)
```

### F4-K005 unknown agent-key revocation

`KeyStore.Revoke` now returns `(revoked bool, err error)` and the handler distinguishes
three outcomes: revoked (200, `revoked: true`), already revoked (200, `revoked: false`, no
second event), unknown (**404**, no event). No revocation evidence is written for a no-op.

### F4-K006 registration evidence

`KeyStore.RegisterWithEvidence` writes the key, the evidence row and the outbox row in one
transaction, through a new `evidence.AppendInTx` — which also replaced a second copy of that
SQL that had drifted inside the policy store. Revocation stays best effort, deliberately and
now documented: containment must not wait on a secondary store, and a key believed
compromised has to stop verifying whether or not the evidence write succeeds.

---

## PUBLIC DECIMAL CONTRACT

**Option A**: quoted decimal strings are officially supported, because several languages
cannot render every supported decimal exactly as a JSON number and a caller who has the
right value should not lose it to their encoder.

`packages/envelope-schema/schemas/agent-execution-envelope.v0.1.json` now declares, for
`notional`, `limit_price`, `stop_price` and `quantity`: the accepted types
(`number | string | null`), the pattern, the scale, and what is refused. `openapi.json` is
regenerated from it. `docs/api/README.md` states the contract in one place:

```text
notional, limit_price, stop_price   scale 4   (0.0001)
quantity                            scale 8   (0.00000001)
exponent notation                   refused
precision beyond the scale          refused (AMOUNT_PRECISION_UNSUPPORTED), never rounded
magnitude                           <= ~461 trillion (money), ~46 billion (quantity)
```

---

## DOCUMENTATION TRUTH

**Ranges corrected.** The parser refuses a whole part above `2^62 / scale` so the conversion
cannot overflow, which is about **461 trillion** for `money.Amount` and **46 billion** for
`money.Quantity` — not the 922 trillion and 92 billion an int64 would hold. The guard stays;
the comments were describing the type rather than the platform. Corrected in
`internal/money/money.go`, `internal/money/quantity.go` and the third report.

**Chaos history corrected.** The second remediation report was delivered with that line
reading "stops containers, runs alone; not run this round". The suite was run afterwards, in
commit `cd97393`, and that report's matrix was amended to PASS — after the audit had read
it. The third report's sentence ("it also ran during the second remediation round and the
report's matrix has said PASS since then") was true of the file and misleading about what
the auditor was given. The third report now says exactly that.

---

## FULL TEST MATRIX

| Item | Result |
|---|---|
| `scripts/verify.sh` (gate: gofmt, vet, eslint, ruff, tsc, mypy, go test, pytest, build, next build) | **PASS** |
| `go test ./...` (14 packages) | **PASS** |
| integration (`tags=integration`) — 114 top-level, 15 subtests | **PASS** |
| race detector over unit + security + integration | **PASS** |
| chaos, 9 scenarios, in isolation | **PASS** |
| process, real gateway binaries | **PASS** |
| two real gateway processes: grant ceiling | **PASS** |
| two real gateway processes: policy branch | **PASS** |
| real process crash/restart | **PASS** |
| 1,000-agent load (p50 4.41 s, p95 4.92 s over HTTP) | **PASS** |
| sustained load ≥ 2 min (25,867 decisions, p95 272–306 ms, 100% ACCEPTED) | **PASS** |
| multi-tenant isolation under load | **PASS** |
| exact-money property | **PASS** |
| signed/economic consistency | **PASS** |
| exact policy thresholds | **PASS** |
| F4 idempotency prune duplicate tests (6) | **PASS** |
| F4 envelope tombstone tests | **PASS** |
| activation tamper | **PASS** |
| activation evidence durability | **PASS** |
| activation restart | **PASS** |
| activation stale predecessor | **PASS** |
| activation predecessor hash | **PASS** |
| activation concurrent branches | **PASS** |
| activation two-process conflict | **PASS** |
| archive `payload.signature` | **PASS** |
| retention export/restore | **PASS** |
| JetStream sink-failure redelivery | **PASS** |
| consumer restart redelivery | **PASS** |
| outbox 2-minute sustained concurrent capacity | **PASS** |
| outbox multi-worker claim safety | **PASS** |
| outbox NATS outage/recovery | **PASS** |
| Alpaca Paper (`tags="integration live"`) | **PASS** |
| live binary boot + key-endpoint smoke (8 checks) | **PASS** |
| producer → outbox → NATS → consumer → projection | **PASS** |
| SPIRE/mTLS, 9 cases | **PASS** |
| console behavioural tests (`tags=console`) | **PASS** |
| simulations end to end (`SIMULATOR_PYTHON`) | **PASS** |

**FAIL** — none.

**SKIPPED** — 5, each stated:

```text
TestLiveAlphaVantageQuote               needs a market-data key §35 keeps out of the repo
TestLiveAdapterRefusesShortWindows      same
TestTheConsoleFieldsExistInLiveResponses        needs GATEWAY_URL + CONSOLE_API_TOKEN
TestTheConsoleFleetFieldsExistInLiveResponses   same, plus FLEET_ENGINE_URL
TestTheConsoleIncidentFieldsExistInLiveResponses same
```

The three console field-contract skips run when the live stack is up; they were exercised
in that configuration during this pass and pass there. The matrix line above reports the
run in which the whole integration suite is executed against a clean database.

**NOT RUN** — none.

---

## BENCHMARKS

| Measurement | Before | After |
|---|---|---|
| Outbox service rate, 1 worker | 1,207/s | **2,493/s** |
| Outbox depth at 2,500/s arrival, 120 s | grew to 26,000+ and beyond | mean ~190, no trend |
| Oldest unpublished age under sustained load | 50 s and rising | ≤ ~250 ms |
| Catch-up after the producer stops | 10,000 rows still queued after 2 min | 202 ms, zero |
| Sustained decision rate | 215/s, p95 272–306 ms | unchanged |
| 1,000 agents, end to end over HTTP | p50 4.41 s, p95 4.92 s | unchanged |

---

## MIGRATIONS

```text
0029_idempotency_tombstones.sql        permanent key and envelope identity
0030_policy_current.sql                the current-policy pointer and transition_seq
0031_activation_key_hardening.sql      one bootstrap grant per tenant, ever
```

Each has a `.down.sql` that states what is lost. `0030` backfills `transition_seq` by the
old ordering and populates the pointer for existing tenants. `0031` fails loudly if a tenant
already holds two bootstrap grants: which key should remain is a question about who holds
policy authority for that customer, and not one a migration may answer.

---

## ADRs

```text
ADR-027   amended: idempotency_tombstones is the tombstone; the pre-broker boundary is
          the durable execution claim
ADR-028   unchanged in substance; its bootstrap rule is now enforced by the database
```

---

## FILES CHANGED

44 files, +3,801 / −267, in seven commits:

```text
505138c  F4-B001  permanent idempotency lives in the idempotency domain
b2ce294  F4-B002  a tenant's policy history is one serialized chain
3262b8b  F4-B003  acknowledge after the insert, not before
65acac4  F4-B004  prove steady-state outbox capacity, and reach it
05178a7  K001-K006 the key endpoints, made safe
00b1b8a  R001/R002 say what the wire accepts, and what the range is
c507309  the bootstrap closes on any existing key
```

---

## DEVIATIONS

1. **`OUTBOX_WORKERS` was added as configuration.** The handoff permits it if multiple
   workers are required; they are not — one worker meets the target after the publishing fix
   — so the default is 1 and the setting exists for deployments above this build's envelope.

2. **F4-K002 is stricter than the addendum asked.** The addendum says no unsigned field may
   change key id, public key, holder or validity. The implementation refuses *any* field
   beside the signed authorization, rather than enumerating which ones are harmless.

3. **The losing gateway in F4-POLICY-05 answers `POLICY_UNAVAILABLE`.** A process whose
   first policy reload was refused has nothing it is entitled to enforce. The test asserts
   the property that matters — it never accepts an order the in-force policy denies — rather
   than requiring it to serve.

---

## OPEN RISKS

1. **Retention has no scheduler.** `Prune` and the archive path exist and are proved;
   running them is still a manual act. The tombstone makes an unrun sweep safe rather than
   dangerous, which was not true before this pass.
2. **`authority_usage` grows without bound.** One row per economic request, permanently
   (ADR-027). Now genuinely redundant with the tombstone for the identity question, and a
   candidate for compaction to identity alone.
3. **No endpoint lists registered keys.** Rotation is done blind: an operator cannot see
   what is registered without database access.
4. **The console has no human reviewer.** Its behaviour is tested; nobody has looked at it.
5. **The capacity envelope is one host.** 2,493 events/s is this workstation with
   PostgreSQL, NATS and the test in one machine. A deployment measurement is not a
   substitute for a production one.

---

## V0 RE-AUDIT REQUEST

Every blocker and every addendum finding has a red reproduction, a fix, and a measurement.
Three defects were found by running rather than by reading — the market-order permanence
gap, the outbox's real service rate, and the bootstrap that ignored keys it had not created
— and the third of those was found by the live smoke test *after* the unit and integration
suites were green, which is the argument for keeping it.

V0 is not self-accepted. A fifth audit is requested against build `c507309`.
