# V0 THIRD AUDIT REMEDIATION

**Build:** `c9d6246` · **Date:** 2026-08-29 · **Environment:** Windows 11 workstation,
Docker (PostgreSQL 16, ClickHouse 25.3, NATS 2, MinIO), Go 1.25, Alpaca Paper.

## STATUS

Every blocker and every required test in the handoff was implemented and run. Four
defects were found by the new tests and the new runs rather than by the audit — the
reason this report requests a re-audit rather than acceptance.

---

## T3-B001 EXACT FINANCIAL CONTRACT

**Pre-fix reproduction.** `900000000000.0002` and `900000000000.0003` are different
amounts, both exactly representable at the platform's scale, and both decoded to
`900000000000.0002`. Authority then authorized `900000000000.0002` for an agent that had
signed the other. The signature covers the decimal token; authorization evaluated a
binary approximation of it.

**Wire representation.** `Intent.Notional`, `LimitPrice` and `StopPrice` are
`money.Amount`, decoded from the JSON literal directly. Excess precision is refused at the
boundary with `AMOUNT_PRECISION_UNSUPPORTED` — a well-formed envelope carrying an
unrepresentable amount is not a malformed envelope, and only the caller can say which of
the two neighbouring amounts they meant.

**Quantity representation.** `money.Quantity`, scale 8. A separate type because a share is
not money and venues quote fractional equity to nine places; sharing one type would force
money to carry a share's precision or a share to be rounded to money's.

**Policy representation.** Every notional threshold — `notional_gt`, `_gte`, `_lt`,
`_lte`, `require_notional_lte`, `_gte` — is `money.Amount` through authoring, compilation
and evaluation. A threshold and a grant ceiling now ask the same question of one order.
`money.Amount` implements `UnmarshalYAML`, without which the decoder would have read a
policy author's `5000` as five thousand ten-thousandths.

**Execution and broker representation.** `broker.OrderRequest` carries the exact types.
Alpaca and Tradier render them with `String()` — decimal text, which is what those venues
accept anyway. `FormatFloat` rendered whatever binary64 had made of the value.

**Signature/economic consistency.** `money.NotionalOf(price, quantity)` multiplies in
`big.Int` and rounds up, away from zero, because a ceiling must count at least what an
order can cost. A product beyond the supported range returns indeterminate rather than
wrapping an enormous order into a small number. `money.FromFloat` survives only for
analytical edges.

**Tests.**

```
TestASignedAmountSurvivesDecoding          two colliding literals stay distinct
TestExcessPrecisionIsRefusedAtDecode       100.00001 refused, by name
TestAnAmountRoundTripsThroughJSON          the literal survives serialisation
TestPolicyThresholdsCompareExactly         12 cases across GT/GTE/LT/LTE at 1000.0001
TestQuantityTimesPriceRoundsUp             7 cases including the documented rounding
TestAnUnrepresentableProductIsNotSilentlyWrapped
TestNoFinancialFloatsOnTheDecisionPath     structural; verified RED
TestFromFloatIsNotOnTheDecisionPath        structural
```

**Two traps found while doing it, both the defect's own shape.** A bare numeric constant
compared against `money.Amount` means ten-thousandths, so `GrossNotional <= 10000.0` was
passing against one currency unit. And the new test's own harness decoded through
`map[string]any`, turning the literal into a float before re-encoding it — the defect, one
layer up, inside the test written to catch it.

---

## T3-B002 SIGNED POLICY ACTIVATION

**Pre-fix tamper reproduction.** A correctly signed SHADOW bundle, promoted by editing one
word of its activation block, took effect. The policy signature still verified because it
covers the rules and the rules had not changed.

**Activation authority model.** Content identity and activation authority are different
facts, signed separately. The bundle signature keeps covering only the rules, so promotion
does not change a bundle's identity; a second signed document says the customer authorized
*this* bundle, named by content hash, to enforce.

**Activation schema.** `policy.Authorization`: schema version, tenant, bundle id, bundle
content hash, prior bundle id and hash, action (`ACTIVATE` / `ROLLBACK`), actor, reason,
authorized-at, nonce, and an Ed25519 signature over the canonical form minus itself.

**Key scope.** `policy_activation_keys`, its own registry (migration 0026). Deliberately
not the agent key store: promoting a policy is an operator's act, an autonomous trading
agent is not an operator, and one registry would let a compromised agent key decide what
constrains it — INV-009 inverted. Keys carry a holder, a validity window and a revocation
with an author.

**Verification.** A bundle enforces only when its signature verifies, it belongs to the
tenant, it is ACTIVE, an authorization names it by content hash, that authorization
verifies against a registered key that is neither revoked nor expired, its nonce is
unseen, and the transition commits. Any failure leaves what was already in force exactly
as it was, and now says so through a report callback — a silent refusal is how an operator
finds out days later that what they staged never took effect.

**Tests.**

```
TestEditingActivationStatusDoesNotActivate    T3-POLICY-01
TestEditingTheAuthorizationIsRefused          T3-POLICY-02, five fields
TestAnAuthorizationCannotCrossTenants         T3-POLICY-03
TestARevokedKeyCannotActivate                 T3-POLICY-04
TestAReplayedAuthorizationIsRefused           T3-POLICY-05
TestARollbackIsSeparatelyAuthorized           T3-POLICY-06
TestARollbackMustNameWhatIsInForce
TestAnActivationEventIsPublishable
```

---

## T3-B003 ACTIVATION EVIDENCE DURABILITY

**Previous failure mode.** `Active()` switched the cached bundle, then appended evidence
and discarded the error. A new policy could be enforcing with no record of who authorized
it.

**Persistent transition design.** `policy_activations` holds accepted transitions. The
transition, its evidence event and its outbox row are one `INSERT ... INSERT ... INSERT`
in a single transaction; enforcement changes only after it commits. The nonce is part of
the primary key, so a replay is a database conflict rather than a decision each process
makes for itself.

**Restart semantics.** Activation versus rollback is what the customer's authorization
says, not what the process witnessed. It used to be a process-local set of bundle ids, so
a restart recorded an activation the customer never performed.

**Multi-replica semantics.** Both replicas read the same accepted transitions. A bundle
already in force under an accepted transition is loaded without requiring a fresh
authorization, so a restart does not need the customer to re-sign anything.

**Failure injection.** With the activation store removed — which is what an outage looks
like from here — the previously authorized bundle stays in force and the staged one does
not take effect.

**Tests.** `TestARestartDoesNotFabricateAnActivation`,
`TestAFailedTransitionLeavesTheOldPolicyEnforcing`.

**A defect the live run found.** Under a thousand concurrent submissions every goroutine
saw the same changed file and all of them presented the same authorization; one won and
218 of 3,000 requests were refused as replays of their own authorization. The reload is
single-flight now, and a lost race re-reads the accepted transition instead of calling it
a replay. 3,000 of 3,000 afterwards.

---

## T3-B004 ARCHIVE CANONICALIZATION

**Pre-fix signature-field tamper.** Two archived events differing only in
`payload.signature` hashed identically.

**Generic canonical JSON.** `internal/canonicaljson` sorts keys, minifies, keeps number
literals verbatim, and **removes nothing**.

**Envelope canonical wrapper.** `intent.Canonical` decodes, drops the envelope's own
signature — a signature cannot cover itself — and calls the generic form. The removal
lives in the domain that needs it.

**Archive proof.** Retention canonicalizes payloads through the generic form. Verified RED
by putting the deletion back: the two events hashed the same again.

**Tests.** `TestAPayloadSignatureIsCoveredByTheArchiveHash` (T3-ARCHIVE-01),
`TestANestedSignatureIsCoveredByTheArchiveHash` (T3-ARCHIVE-02),
`TestReorderedKeysStillVerify` (T3-ARCHIVE-03), `TestAChangedPayloadBreaksTheChain`, and
the seven-case MinIO export/restore suite unchanged and passing.

---

## T3-B005 REAL MULTI-REPLICA

```
processes started     2, from the built binary (tests/process)
ports                 127.0.0.1:50891 and 127.0.0.1:50894 (ephemeral per run)
shared dependencies   PostgreSQL only
attempts              6 x 4,000 = 24,000, alternating between processes
allowed               2
ledger                8,000.0000 exact
result                PASS
```

The in-process test is kept. This is added beside it, because a test whose comments say
"a separate process" while the implementation is an `httptest.Server` is a defect in the
record even when the property holds.

---

## T3-B006 REAL CRASH RECOVERY

```
crash point                  ASSURANCE_TEST_CRASH_POINT=after_broker_accept_before_outcome_commit
                             os.Exit(9); refused unless ASSURANCE_ENV=development
venue                        Alpaca Paper — a fake broker dies with the process
venue submissions before     1
gateway                      killed mid-request (connection forcibly closed)
record after the crash       PENDING
restart                      new process, same idempotency key
venue submissions after      1
reconciliation               outcome ACCEPTED, record RESOLVED
reservation                  settled; open orders 1, consumed 4000.0000
evidence                     no submission attempt recorded by the recovering process
result                       PASS
```

---

## T3-B007 CHAOS

```
command    go test -tags=chaos -count=1 -timeout 15m ./tests/chaos/...
```

```
TestEnforcementSurvivesClickHouseOutage      PASS
TestEnforcementSurvivesNATSOutage            PASS
TestEnforcementSurvivesRedisOutage           PASS
TestEnforcementSurvivesIntelligenceOutage    PASS
TestPostgresOutageFailsClosed                PASS
TestMissingPolicyBundleDenies                PASS
TestBrokerTimeoutDoesNotDuplicate            PASS
TestGatewayRestartLosesNothingThatMatters    PASS
```

Run in isolation; the suite stops real containers. It also ran during the second
remediation round and the report's matrix has said PASS since then.

---

## T3-R001 OUTBOX CAPACITY

**Old service rate.** 100–200 events/s against arrivals of about 2,200. One batch of 100
per one-second tick made the rate a constant regardless of depth; marking cost two round
trips per event; and a read with no claim let two publishers take the same rows.

**New design.** `FOR UPDATE SKIP LOCKED` with an expiring lease (migration 0027), one
statement to mark a whole drained batch, and a drain that continues while a backlog
exists rather than once per tick. Defaults moved to a 500 batch and a 250 ms idle wait.

```
arrival rate         1,028 events/s (10,000 events)
peak queue depth     500 (5% of the run in flight)
oldest event age     530 ms at peak
catch-up             195 ms after the last arrival
duplicates           tolerated by design; the consumer deduplicates by event id
loss                 none; the queue converged to zero
result               PASS
```

**Projection lag.** The consumer was then the bottleneck: one HTTP insert per event, a few
hundred a second. It fetches 500 and inserts them in one statement, before acknowledging.
On the standing backlog: 394,688 rows projected, 675,927 twenty seconds later. On a live
run: 25,000 queued fell to 499 and 29,001 events reached ClickHouse within thirty seconds
of the load stopping.

**Multiple publishers.** `TestTwoPublishersDoNotDrainTheSameRows`: publisher A claimed 40,
publisher B claimed 0, overlap 0.

---

## T3-R002 IDEMPOTENCY/RESERVATION RETENTION

**Chosen contract.** An idempotency key identifies one economic request, permanently.
Retention prunes the record — the cached outcome and the ability to replay it — and does
not reopen the key. `authority_usage` is the tombstone.

**ADR.** ADR-027, amending ADR-015.

**Implementation.** No code change was needed: the behaviour was already this. What
changed is that the prose no longer contradicts it. `execution/retention.go` and the
runbook said pruning reopened the key and a later caller got a fresh execution;
`authority_usage` refused. The platform stated both, and which one a caller met depended
on which layer answered first.

The alternative was rejected on its merits: it needs two sweeps in every replica to forget
the same key at the same moment, and either ordering is a defect — one of them the
duplicate execution INV-004 exists to prevent.

**Tests.** `TestAPrunedRecordDoesNotReopenItsKey` claims, resolves, prunes and presents
the key again: refused with `RESERVATION_KEY_REUSED`.

---

## T3-R003 AMOUNT PARSE FAILURE

**Implementation.** `parseAmount` returns its error. The reservation aborts, `Evaluate`
denies with `USAGE_UNAVAILABLE`, and a key holding an unreadable amount is refused as a
reuse rather than treated as a match — "we cannot read what this key holds" must not
resolve to "it holds what you are asking for".

**Fail-closed proof.** `TestUnreadableUsageDenies` injects the failure at the repository
boundary, because a `numeric(20,4)` column cannot express the corruption directly. It
asserts the denial and the code.

---

## T3-R004 SECURITY DOWN MIGRATION

**Decision.** A rollback of a least-privilege migration does not silently restore the
surface it removed.

**Implementation.** `0025_outbox_publisher_role.down.sql` drops only what is safe to drop
— the publisher's policies and grants — and leaves the tenant-scoped application policy in
place. The old broad-read policy is written out in a comment, so an operator who genuinely
needs it types it having read why they should not.

---

## DOCUMENTATION TRUTH

**Files updated.** `docs/PROJECT_STATUS.md`, `docs/operations/README.md`,
`internal/execution/retention.go`, `.env.example`, `MASTER_BUILD_SPEC.md`, `Makefile`.

**Current measured figures.** Superseded numbers are labelled historical and kept:

```
1,000 agents x 3 signed intents   3,000 accepted, 288/s, p50 3.39s over HTTP
sustained, 2 minutes              33,227 decisions, 276/s, p50 174ms p95 208ms p99 231ms
multi-tenant                      two tenants, neither listing the other's intents
signature verification            p50 76µs   (batched; below the clock's resolution)
authority reservation             p50 5.5ms  p99 7.9ms
evidence receipt                  p50 9.8ms  p99 13.5ms
idempotency claim                 p50 3.8ms  p99 6.5ms
outbox                            1,028/s in, peak depth 5%, catch-up 195ms
```

PROJECT_STATUS previously reported 475/s and 428/s from a build with no signature
verification, no atomic reservation, no decision receipt and no authorized activation.
The fall is explained rather than buried, and the historical table says so.

---

## FULL TEST MATRIX

**PASS**

```
verify (gofmt, vet, lint, unit, guards, console build)          PASS
go test ./...                                                   PASS
integration                                                     PASS   47s
race (in a container)                                           PASS
chaos (8 scenarios, in isolation)                               PASS
two REAL gateway processes                                      PASS
real process crash/restart recovery (Alpaca Paper)              PASS
1,000-agent load, current build                                 PASS
sustained load, current build                                   PASS
multi-tenant load, current build                                PASS
exact-money property tests                                      PASS
signed-intent/economic-value consistency                        PASS
exact policy threshold tests                                    PASS
policy activation tamper tests                                  PASS
policy activation evidence failure test                         PASS
gateway restart activation truth test                           PASS
multi-replica policy activation truth test                      PASS
archive payload.signature tamper test                           PASS
full retention export/restore tests (7 cases, MinIO)            PASS
outbox sustained throughput + lag test                          PASS
Alpaca Paper, current build                                     PASS
live binary boot                                                PASS
producer -> outbox -> NATS -> consumer -> ClickHouse projection  PASS
SPIRE / mTLS submission (10 cases)                              PASS
```

**FAIL** — none outstanding.

**SKIPPED** — none. Every mandatory item above was run on this build.

**NOT RUN**

```
A3 attestation                    never produced in V0, by design
destructive purge                 disabled by decision; the gate is tested, the deletion does not exist
production-representative load    every figure is a laptop talking to containers on the same laptop
console-web behavioural review    it builds and its API contract is unchanged; nobody drove the UI
```

---

## BENCHMARKS

Local Docker on a Windows workstation. Absolute numbers describe this machine.

| Stage | p50 | p95 | p99 |
|---|---|---|---|
| Signature verification | 76 µs | 106 µs | 107 µs |
| Authority reservation | 5.5 ms | 7.0 ms | 7.9 ms |
| Evidence receipt (6 events + outbox) | 9.8 ms | 12.1 ms | 13.5 ms |
| Idempotency claim | 3.8 ms | 5.4 ms | 6.5 ms |
| End to end, sustained load | 174 ms | 208 ms | 231 ms |

Signature verification is timed in batches of 200 and divided: one verification is below
this platform's ~0.5 ms clock resolution, and per-call timing reported a p50 of exactly
zero — a figure produced by the clock rather than by the code.

---

## MIGRATIONS

```
0026_policy_activation.sql       policy_activation_keys, policy_activations
0027_outbox_lease.sql            claimed_at / claimed_by, claimable index
0025_..._role.down.sql           rewritten: no silent security downgrade
```

Both new migrations have `.down.sql`. 0026's down destroys the record of who authorized
which policy, and says so.

---

## ADRs

```
ADR-027  An idempotency key is never reusable (amends ADR-015)
```

---

## FILES CHANGED

132 files, +6,156 / −671 across eight commits:

```
e07b8c5  brand: establish EXORYN brand system v1 (isolated; no product code)
bddb1ef  T3-B001 exact money from the wire to the venue
9bd7243  T3-B004 archive canonicalization; T3-R003 fail closed; T3-R004 down migration
2673350  T3-B002 signed activation; T3-B003 durable transition; T3-R001 outbox capacity
16f8380  T3-B005 two real processes; T3-B006 real crash/restart
86ec4ca  T3-R002 ADR-027; T3-R005 current figures
5e0b176  the activation event no consumer could read; the capacity metric
c9d6246  the projection was the next bottleneck
```

---

## DEVIATIONS

1. **The crash test uses Alpaca Paper rather than a fake venue as a separate service.**
   The handoff suggested a fake venue process; a real paper venue is strictly stronger,
   because the order genuinely persists across the kill. No fake-venue service was built.
2. **One signing key across the synthetic load fleet.** A thousand agent identities with
   a thousand grants, one key pair. Generating a thousand pairs would measure key
   generation.
3. **The two-process test uses ephemeral ports**, not fixed 8073/8074, so it can run
   beside a live gateway.
4. **`money.Amount` reaches about 922 trillion.** The handoff's example value,
   `1234567890123456.7890`, is outside that range; the colliding pair used in the
   reproduction is inside it and demonstrates the same defect.
5. **Quantity scale is 8, price and notional scale is 4.** Stated because the handoff
   asked for explicit scales and they are not the same.

---

## OPEN RISKS

1. ~~**No endpoint registers an agent signing key or a policy activation key.**~~
   **CLOSED after this report.** `POST /v1/agent-keys` and `POST
   /v1/policy-activation-keys` (with their revocations) were built on 2026-08-30, each
   behind a privilege of its own: `GATEWAY_KEY_REGISTRARS` and
   `GATEWAY_ACTIVATION_KEY_REGISTRARS`, both separate from `GATEWAY_GRANT_ISSUERS`.
   Activation keys additionally bootstrap once and thereafter extend only by the
   customer's own signature (ADR-028). Neither onboarding needs database access.
2. **Sustained peak still exceeds one publisher.** The controlled measurement shows depth
   staying at 5% and catching up in 195 ms; the synthetic load harness drives far more
   traffic than the platform is sized for, and at that rate a single publisher falls
   behind during the run and clears afterwards. The lease makes running several
   publishers safe; nothing runs more than one today.
3. **Retention has no scheduler.** The archive path exists and is proved; running it is a
   manual act.
4. **The `authority_usage` tombstone grows without bound.** One row per economic request,
   permanently, which ADR-027 accepts for V0. Compacting old rows to identity alone is a
   next-phase item.
5. **`evidence_outbox` reads are cross-tenant for the publisher role.** Narrowed from the
   whole application to a `SELECT`/`UPDATE`-only role, and still the one place a role
   sees every tenant.
6. **console-web was not driven.** It builds, and the API contract it consumes is
   unchanged; the semantic question — that an operator now reads
   `execution.decision.committed.v1` where they used to read `broker.order.submitted.v1` —
   is a UI review nobody has done.
7. **Rollback detection depends on the customer's authorization being honest about its
   predecessor.** The platform refuses a rollback naming a bundle that is not in force; it
   cannot detect one that names the right predecessor for the wrong reason.

---

## V0 RE-AUDIT REQUEST

**THIRD REMEDIATION COMPLETE. REQUESTING THIRD RE-AUDIT** against `c9d6246`.

Not requesting acceptance.

Four defects closed this round were found by the work rather than by the audit: the
activation race that refused 218 of 3,000 requests under load, the activation event queued
in a shape no consumer could read, the projection becoming the bottleneck once the queue
in front of it was fixed, and the capacity metric that was wrong in three successive
formulations before it measured anything. Every one of them was invisible to code reading
and to the test suite as it stood, and three of the four appeared only when the deployable
ran under load with every dependency attached.

The four equalities the handoff asked for hold, and are tested:

```
the amount signed == authorized == policy-evaluated == reserved == submitted
the policy active == the policy the customer authorized == what the evidence says
the event archived == the complete immutable event whose hash was verified
a test described as two processes == two actual OS processes
```

What that establishes is that these specific statements are true of this build. The
defects that remain are, by construction, the ones no test written so far is shaped to
see.
