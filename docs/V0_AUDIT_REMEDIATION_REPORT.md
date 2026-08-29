# AUDIT REMEDIATION

**Against:** `V0_AUDIT_REMEDIATION_HANDOFF.md`, audit dated 2026-08-29
**Repository:** agentic-assurance, 8 commits from `3a90d12` to `d808ad7`, 53 files
**Status:** the two blockers and R-003 through R-006 are implemented and verified;
R-009's retention foundation is built with destructive purge off by default.
**This does not declare V0 accepted.** It returns evidence and requests audit.

Every claim below was verified before it was accepted. The audit was right about all
seven findings, and one of them — B-002 — was reproduced as a working failure before a
line was changed.

---

## BLOCKER B-002 — AUTHORITY LIMIT CHECK/RECORD IS NOT ATOMIC

### Reproduction, before any change

`tests/integration/authority_concurrency_test.go`, against real PostgreSQL: a grant with
a 10,000 rolling hourly ceiling, four concurrent submissions of 4,000 each.

```
4 of 4 intents were allowed; 4 orders reached the venue;
ceiling is 10000 and each order is 4000
FAIL: 16000 reached the venue against a rolling ceiling of 10000
```

Every operation was race-free and the invariant was violated. INV-002 is a property of
the system; a ledger that never loses a concurrent write is not a limit. **A limit is a
decision nobody else can make at the same moment.**

### Implementation

`internal/authority/reservation.go`, `usage.go`, migration `0017`.

Authorization of size is now a reservation. `Reserve` takes an advisory lock on
`tenant:grant`, counts what is held, evaluates the limits, inserts the row and commits —
one transaction. It sits immediately before the venue call and after every cheaper
refusal, so capacity is never held for an order that policy was going to deny. Nothing
reaches a venue without holding one, and a reservation that cannot commit denies
(`USAGE_UNAVAILABLE`).

Reservation states, as the audit's B-002.4 and B-002.5 asked:

| State | When | Effect |
|---|---|---|
| `RESERVED` | held, outcome unknown | counts against every limit |
| `COMMITTED` | venue accepted | counts for as long as the window holds it |
| `RELEASED` | **definite** venue rejection | returns the capacity |

A definite rejection releases because the order does not exist, and leaving it consumed
would let anyone exhaust a customer's grant with requests a venue was always going to
refuse. An **ambiguous outcome never releases**: the reservation is held until
reconciliation resolves it, because releasing capacity for an order that may be working
is how an unknown outcome becomes an exceeded ceiling (INV-004).

Authority's earlier evaluation stays as a cheap pre-check that fails fast. Both paths
share one arithmetic function, so they cannot disagree about what a limit means. The old
`UsageRecorder` is deleted rather than left as a field nothing calls.

### Concurrency proof

Same test, after:

```
2 of 4 intents were allowed; 8000 of a 10000 ceiling
PASS
```

Cross-replica correctness rests on the advisory lock being in PostgreSQL rather than in
a process. **Not yet run against two gateway processes sharing one database** — the
mechanism is shared-database rather than in-memory, and the audit's multi-instance run
is listed as NOT RUN below rather than claimed.

---

## BLOCKER B-001 — EXECUTABLE ENVELOPE SIGNATURE IS NOT VERIFIED

### What was true

The envelope carried a `Signature` field no code read, the schema did not require it,
and `TestSignatureVerificationIsNotYetImplemented` skipped with a comment saying the
feature had no owning phase — for eleven phases. Section 12.2 locks
`invalid signature -> DENY`.

The consequence was not cosmetic. Transport identity establishes the **tenant**; the
**agent** was a claim in the body, while an authority grant is scoped to exactly that
agent. An authenticated caller could submit under any agent id it liked.

### Implementation

`internal/intent/signature.go`, `internal/identity/agentkeys.go`, `keystore.go`,
migration `0018`, pipeline stage in `internal/gateway/pipeline.go`.

- **Key registry** scoped by `(tenant_id, agent_id, key_id)` — the primary key, not a
  check somebody remembers to write. Ed25519 only: an algorithm the caller names is a
  downgrade attack unless the platform decides which values are acceptable. Revoked keys
  are kept rather than deleted, because an envelope signed last week was signed by a key
  that was valid last week.
- **Canonical form v0.1**, versioned and small enough to reimplement in any language:
  remove the `signature` member, sort keys by code point, minify, and emit number
  literals **verbatim**. That last rule is the one that matters — re-encoding `1200` as
  `1200.0` through a float is how a correct signature stops verifying — and it has test
  vectors.
- **Pipeline order**: decode → validate → transport identity → tenant → **signature** →
  idempotency → authority → controls → parent intent → policy → reservation → execution.
- **Codes**: `SIGNATURE_MISSING`, `SIGNATURE_KEY_UNKNOWN`, `SIGNATURE_KEY_REVOKED`,
  `SIGNATURE_KEY_EXPIRED`, `SIGNATURE_ALGORITHM_UNSUPPORTED`, `SIGNATURE_INVALID`.
  An unknown key and a key belonging to another agent answer identically, so the error
  cannot be used to enumerate a tenant's agents.

### Tests

All thirteen cases from B-001.7, in `internal/gateway/signature_test.go`:

| Case | Result |
|---|---|
| valid signature | proceeds |
| missing signature | DENY |
| wrong agent's key | `SIGNATURE_KEY_UNKNOWN` |
| revoked key | `SIGNATURE_KEY_REVOKED` |
| expired key | `SIGNATURE_KEY_EXPIRED` |
| unsupported algorithm | `SIGNATURE_ALGORITHM_UNSUPPORTED` |
| notional changed after signing | DENY, nothing sent |
| instrument changed | DENY, nothing sent |
| `authority_grant_id` changed | DENY, nothing sent |
| idempotency key changed | DENY, nothing sent |
| agent changed | DENY, nothing sent |
| tenant changed | DENY, nothing sent |
| same envelope, 20 verifications | deterministic |

The schema requires `signature` with `algorithm`, `key_id` and `value`; validation
requires the shape; verification requires the cryptography. Every fixture and every test
harness signs, because a harness that skipped verification would exercise a pipeline
nobody runs.

**One deviation from B-001.5 worth naming:** an envelope with no signature is refused at
validation as `SIGNATURE_REQUIRED` rather than at the signature stage as
`SIGNATURE_MISSING`, because the schema marks the member required and validation runs
first. Both deny before anything executes.

---

## R-003 CAUSATION

`evidence.Event` carried `CausationID` since Phase 6 and the real producer never set it:
an integration test built a chain by hand and passed, so the field was supported by
everything except the thing that emits events.

The per-submission recorder now sets each event's causation to its predecessor's id.
`TestTheChainOfEventsNamesItsPredecessor` walks an actual submission — first event has
empty causation, every other names the one before it — rather than handcrafted events.

---

## R-004 EVIDENCE DURABILITY

### Failure model

Two classes, and conflating them was the defect:

- **Decision receipt** — everything up to and including the order being sent.
  Authoritative. Committed **before** the venue is called. If it cannot commit, the
  submission does not happen (`EVIDENCE_UNAVAILABLE`), because an order at a venue that
  the platform has no record of deciding is the state an assurance layer exists to make
  impossible.
- **Outcome record** — what the venue answered. Recoverable from the receipt plus
  reconciliation, since the idempotency record is claimed before the venue call.

Neither is telemetry. Section 17's "production continues when telemetry is unavailable"
covers the analytical plane.

### Proof

- `TestAnUnrecordableDecisionNeverReachesTheVenue`: evidence sink broken → refused, zero
  submissions at the venue.
- `TestLosingThePostExecutionRecordDoesNotUndoTheOrder`: receipt accepted, outcome write
  fails → order stands, exactly one submission.
- The test that asserted the opposite — that a decision proceeds with the evidence store
  down — was **replaced rather than deleted**, and a security path test that called that
  behaviour INV-005 was split into two: one proves enforcement with the analytical plane
  absent, the other proves a decision that cannot be recorded is not acted on.

**Not implemented:** the crash-and-restart test of R-004.4. The reconstruction path it
exercises exists and is covered by the ambiguous-outcome and reconciliation tests, but
the kill-mid-write scenario is listed as NOT RUN.

---

## R-005 EVENT BACKBONE

### Runtime wiring

`internal/evidence/outbox.go`, migration `0019`, ClickHouse `0008`, wired in both
binaries.

```
decision commits ──┬── evidence_events        (PostgreSQL, the record)
                   └── evidence_outbox        (same transaction)
                            │
                   outbox publisher (gateway, background)
                            │
                     NATS JetStream
                            │
              durable consumer (fleet engine)
                            │
              assurance.evidence_stream (ClickHouse projection)
```

### Outbox semantics

The event and its outbox row are written in the same transaction as the decision they
describe, so a committed decision always owes the bus a message and a message never
exists for a decision that did not commit. A publisher that dies resumes from the table.
Attempts and last error are counted per row. Publication is downstream of a committed
record: a bus that is unreachable delays the analytical plane and decides nothing
(INV-005). At-least-once redelivery is absorbed by a `ReplacingMergeTree` keyed on event
id.

### Tests

`TestCommittedEvidenceReachesTheStreamAndTheProjection` starts where a submission starts
— evidence committed through the store — and ends where an analytical reader looks. It
found a real defect on its first run: marking a row published ran without a tenant
context and row level security silently refused the update, so events published and the
queue never emptied.

**Stated rather than implied:** fleet telemetry still writes to ClickHouse directly.
Evidence flows through the outbox; intent telemetry does not, and the documentation says
so in both `hot-path.md` and `PROJECT_STATUS.md`.

---

## R-006 POLICY RELOAD

`FileBundles.Active` stats the tenant's file per submission and re-reads only when size
or modification time changed. Verification and compilation happen on change, not per
order.

| Case | Behaviour |
|---|---|
| bundle replaced | next decision uses the new one, no restart |
| rolled back to the previous file | next decision uses it |
| candidate signed by somebody else | the bundle already enforcing stays in force |
| candidate for another tenant, or not ACTIVE | same |
| file deleted | the bundle already enforcing stays in force |

A half-applied activation is worse than a late one. Nothing here activates anything: the
customer signs and stages, and this reads what they made active.

---

## RETENTION

### Policy model

Per tenant and per record class — `ORDER_ASSURANCE_EVIDENCE`, `AUTHORITY_GRANT`,
`POLICY_DECISION`, `HUMAN_CONTROL_ACTION`, `SECURITY_ATTESTATION`, `SIMULATION_RECORD`,
`ANALYTICAL_TELEMETRY` — with hot days, archive days, destination and deletion mode.
There is deliberately no mode meaning "delete automatically". An unclassified event
falls into the longest-lived class.

### Partitioning

`evidence_events` is partitioned by month (migration `0020`), rebuilt over 940,000 rows.
Retention is a detach, not a multi-year delete from the table the enforcement path writes
to. The migration is repeatable, which it was not at first — the replay renamed the
partitioned table aside, found every partition name taken and left a parent with no
partitions. It failed inside its transaction and rolled back.

### Archive verification

A manifest records the partition, period, event count, destination and **chain head**.
The chain hashes each event's identity — not its stored bytes, so a re-export produces
the same head — with every hash covering the previous one. Tested by editing one event
in the middle of an archive and by removing one: both change the head.

### Legal hold

A hold is scoped to a tenant and optionally to one correlation id, because an
investigation is usually about a chain rather than a month. It outranks every policy.

### Deletion safeguards

Four rules a configuration cannot override, each with a test:

1. a hold outranks every policy;
2. an unarchived partition is never destroyed;
3. destruction requires an approved authorization, and the approver may not be the
   requester (a database constraint, not a convention);
4. every default keeps everything.

The dry-run planner produces verdicts and reasons without touching anything. **No
destructive purge is enabled**, and nothing in this repository deletes evidence.

---

## STALE SKIPS REMOVED

- `TestSignatureVerificationIsNotYetImplemented` — deleted; the property is implemented
  and covered by thirteen cases.
- `TestReplayHandlingIsPhase5` — deleted; idempotency shipped in Phase 5 and is covered.

Remaining `t.Skip` calls are environment guards only (no interpreter, no infrastructure,
no credentials) and each names what is missing.

---

## FULL TEST MATRIX

| Suite | Result |
|---|---|
| `./scripts/verify.sh` (gofmt, vet, unit, structure, security, contract, scenarios, console, Python) | **PASS** |
| `make test-integration` | **PASS** |
| `make test-race` | **PASS** |
| `make test-chaos` | **PASS** |
| `make test-load` (1,000 agents) | **PASS**, before this pass; see NOT RUN |
| `make test-load-sustained` | **PASS**, before this pass |
| `make test-load-tenants` | **PASS**, before this pass |
| Alpaca Paper live contract check | **PASS**, before this pass |
| New atomic-authority concurrency test | **PASS** |
| New signature test family (13 cases) | **PASS** |
| New causation chain test | **PASS** |
| New evidence-durability tests | **PASS** |
| New runtime outbox → NATS → consumer → projection test | **PASS** |
| New policy reload / rollback tests | **PASS** |
| New retention rules and chain-tamper tests | **PASS** |

**NOT RUN** — and not claimed:

- **Multi-replica grant limit** (audit 12.9): two gateway processes sharing one database.
  The lock is in PostgreSQL rather than in a process, but the two-instance run has not
  been executed.
- **Evidence crash recovery** (audit 12.5, R-004.4): kill between venue acceptance and
  the outcome write.
- **Load suites re-run after this pass.** The last figures predate the signature,
  reservation and receipt work, all three of which add work per submission.
- **Alpaca Paper re-run after this pass**, for the same reason.
- **Retention archive export against object storage.** The chain and its tamper
  detection are tested; a real upload and restore are not.
- **Live boot of the rebuilt gateway binary.** This host's Application Control is
  refusing freshly built executables; the wiring is covered by the runtime integration
  test and by a source-level guard, not by a running process today.

**FAIL:** none.

---

## BENCHMARK DELTA

| | Before this pass | After | Note |
|---|---|---|---|
| Enforcement computation | 12.5 µs p50 | not re-measured | signature verification adds an Ed25519 verify |
| Accepted intent, end to end | 24.6 ms | not re-measured | adds a signature verify, a reservation transaction and a synchronous receipt |
| 1,000 concurrent agents | 475/s | not re-measured | |

Re-measuring is listed above as NOT RUN rather than estimated. The reservation adds one
locked transaction per accepted order, which is the deliberate cost of the invariant.

---

## FILES CHANGED

53 files across 8 commits. New packages: `internal/retention`, `internal/pg`. New
sources of note: `internal/intent/signature.go`, `internal/identity/agentkeys.go`,
`internal/identity/keystore.go`, `internal/authority/reservation.go`,
`internal/evidence/outbox.go`, `internal/fleet/evidence_projection.go`,
`internal/execution/retention.go`.

## MIGRATIONS

| | |
|---|---|
| `0017_authority_reservation` | reservation state on the usage ledger |
| `0018_agent_signing_keys` | the key registry, keyed by tenant + agent + key id |
| `0019_evidence_outbox` | the durable outbox |
| `0020_evidence_partitioned` | evidence partitioned by month, repeatable |
| `0021_retention_policy` | policies, legal holds, manifests, deletion authorizations |
| `clickhouse/0008_evidence_stream` | the analytical projection |

Each has a down migration except `0020`, whose reversal is a maintenance operation
rather than something to run by accident, and which says so.

## NEW ADRs

None. Every change implements a locked requirement rather than altering one; the event
backbone is now what the architecture already said it was, so the audit's alternative —
an ADR changing the locked backbone — was not needed.

## DEVIATIONS FROM MASTER SPEC

1. **A missing signature denies at validation, not at the signature stage.** The schema
   requires the member and validation runs first. Both refuse before anything executes.
2. **Fleet telemetry does not flow through the outbox.** Evidence does. Stated in the
   architecture documents rather than implied away.
3. **The outbox queue is read without a tenant context.** Writes remain tenant-scoped and
   the policy's `WITH CHECK` holds; the publisher drains every tenant's queue, and the
   reasoning is written into the migration rather than left for a reader to infer.

## OPEN RISKS

1. **Cross-replica behaviour is argued, not demonstrated.** The reservation lock is in
   the database; nothing has been run against two gateways.
2. **Performance after this pass is unmeasured.** Three additions per accepted order.
3. **Parent intent remains observational** (audit R-008). Fragmented economic intent is
   detected and recorded; what stops it is the grant's atomic ceiling. The status
   document says this rather than implying prevention.
4. **Retention is a foundation, not an operating policy.** No tenant has one configured,
   nothing archives on a schedule, and no purge exists.
5. **A2 binds a workload to a tenant, and the signature binds an envelope to an agent.**
   Together they cover R-007. What no deployment here registers is a workload-to-agent
   binding, so the two facts are recorded separately rather than merged.

## V0 ACCEPTANCE REQUEST

The two blockers are closed with reproductions before and proofs after. R-003 through
R-006 are implemented with tests that exercise the running path rather than a library.
The retention foundation is built and destroys nothing.

Six items are listed as NOT RUN, and none of them are reported as passing. Audit is
requested on that basis.
