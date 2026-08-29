# Agentic Order-Flow Assurance Platform — status and defect log

A single document for someone who has not seen this repository: what it is, what is
built, how it is verified, what every audit pass found, and what is still open.

Written to be argued with. Where a number appears it was measured on the reference
machine and says so; where something is absent it says that too.

---

## 1. What this is

B2B infrastructure for financial institutions to **attest, authorize, monitor and
contain AI-generated financial order flow before it reaches execution infrastructure**.

It is not a trading system. It has no strategy, no alpha, no opinion about what should
be bought. It sits between an autonomous agent and a venue and answers one question per
order: *may this specific intention, from this specific agent, under this specific
authority, proceed right now* — and leaves an account of why.

**What it is not:** a trading bot, a robo-advisor, an execution algorithm, or an
LLM in the decision path. No model output can influence an enforcement decision
(INV-003): the enforcement plane is deterministic Go, and the intelligence plane can
only recommend.

### The ten principles

| | |
|---|---|
| P-001 | Deterministic enforcement |
| P-002 | Customer-owned authority |
| P-003 | Provenance is first-class |
| P-004 | No fake certainty |
| P-005 | Intent over tool call |
| P-006 | Local safety survives cloud failure |
| P-007 | Audit history is append-only |
| P-008 | Explain before scoring |
| P-009 | Simulation before automation |
| P-010 | Protocol independence |

### The fifteen invariants

| | |
|---|---|
| INV-001 | An unauthenticated workload can never create an executable order |
| INV-002 | An agent can never exercise more authority than its active grant |
| INV-003 | No LLM output can bypass deterministic policy |
| INV-004 | No ambiguous broker timeout may trigger blind duplicate execution |
| INV-005 | Loss of the intelligence cloud cannot disable local hard limits |
| INV-006 | Historical evidence cannot be silently mutated |
| INV-007 | Tenant A cannot observe Tenant B data |
| INV-008 | Unknown provenance can never be represented as verified provenance |
| INV-009 | Fleet intelligence may recommend; customer policy authorizes enforcement |
| INV-010 | A new policy cannot reach production without versioning and validation |
| INV-011 | Redis loss cannot destroy authoritative financial-control state |
| INV-012 | A broker adapter failure cannot corrupt the canonical core domain model |
| INV-013 | Audit logs and application logs are not interchangeable |
| INV-014 | Model identity must never be inferred from workload identity without evidence |
| INV-015 | An invalid instrument normalization result cannot proceed to executable policy |

Each one has at least one test that fails when it is violated. Several of them exist
because a test was written first and found the violation, which the defect log below
records case by case.

---

## 2. Shape

Go 1.25 monorepo, 150 Go files, ~36,000 lines. Four deployables (ADR-011):

| Process | Plane | Owns |
|---|---|---|
| `assurance-gateway` | Enforcement | The only write path to a venue. Identity, authority, fleet controls, policy, idempotency, evidence |
| `fleet-engine` | Intelligence | Fleet risk vector, cohorts, dependencies, incidents, the Digital Twin runner |
| `console-web` | Read-only UI | Six surfaces (Next.js 15, TypeScript strict). No write path, ever |
| `simulator` | Offline | The Digital Twin (Python 3.12 / NumPy) |

### Data stores, and what each one is allowed to be

- **PostgreSQL** — source of truth. Authority grants, idempotency records, evidence,
  incidents, fleet controls. Row Level Security with `FORCE ROW LEVEL SECURITY` on
  every tenant-scoped table; every read and write runs inside a transaction that sets
  `app.tenant_id`, and no store method reads without a tenant.
- **ClickHouse** — analytical only, and **forbidden on the enforcement path** (INV-005).
  A decision must not depend on analytics being reachable.
- **Redis** — cache, never truth (INV-011).
- **NATS JetStream** — event backbone for evidence, fed by a durable outbox: an event
  and its outbox row are written in the same transaction as the decision, a publisher
  drains the table, and a consumer in the fleet engine projects the stream into
  ClickHouse. Nothing on the enforcement path waits for it (INV-005). Fleet telemetry
  still goes to ClickHouse directly, which is stated rather than implied.
- **SPIRE/SPIFFE** — workload identity. X509-SVID verified with stdlib `crypto/x509`.

### The hot path

`docs/architecture/hot-path.md` is the specification for `internal/gateway/pipeline.go`,
not a description of it. If the two disagree the document is right and the code is a
bug. In order:

decode/validate → identity and attestation → tenant check → **envelope signature** →
idempotency → authority → **fleet controls** → parent intent → hard policy →
**atomic authority reservation** → durable decision receipt → execution → outcome
evidence.

### Attestation levels

| | |
|---|---|
| A0 | Unknown origin. Legitimate to observe; never executable |
| A1 | Authenticated API identity (bearer credential bound to a tenant) |
| A2 | Workload-attested (verified X509-SVID mapped to a tenant) |

At every level the envelope itself is signed by a key registered to that tenant **and**
that agent. Transport identity says which customer is calling; the signature says which
agent, which is what the authority grant is scoped to. They are separate provenance
facts and the platform records both.
| A3 | Provider-attested — **never produced in V0**, by ADR-006. Manufacturing it from a workload certificate is exactly the inference that ADR forbids |

---

## 3. The public surface

Generated from the routes the binaries register: `docs/api/openapi.json`, built by
`go run ./cmd/openapi-gen`. A test regenerates and compares, so the document cannot
drift from the code.

```text
POST /v1/intents                        the enforcement plane's only write path
GET  /v1/intents                        recent intents, refusals included
GET  /v1/intents/{id}                   status, or the chain that refused it
GET  /v1/intents/{id}/evidence          the chain for one intent
GET  /v1/evidence?correlation_id={id}   the chain for one correlation id

POST /v1/authority-grants               issue authority (separate privilege)
POST /v1/authority-grants/{id}/revoke   the emergency lever

POST /v1/controls                       authorize a fleet control (INV-009)
GET  /v1/controls                       what is in force, and what was
POST /v1/controls/{id}/revoke           lift one

GET  /v1/fleet/state                    fleet risk vector
GET  /v1/cohorts                        cohorts observed
GET  /v1/dependencies                   declared dependency concentration
GET  /v1/incidents                      incidents opened
GET  /v1/incidents/{id}                 one incident and its timeline

POST /v1/simulations                    start a Digital Twin experiment
GET  /v1/simulations                    runs, newest first
GET  /v1/simulations/{id}               one run with its record
POST /v1/simulations/{id}/cancel        stop a queued or running experiment
```

**Every endpoint that carries tenant data authenticates**, not only the mutating ones.
The tenant comes from the credential (`identity@tenant=token`), never from a header or
a body field; a header that disagrees is `403` rather than ignored.

Three separations are deliberate and each has a reason that cost something to learn:

- **Issuing authority is not submitting intents.** A credential that could do both
  would let an agent raise its own ceiling, and INV-002 would be enforced against a
  limit the party under it can move (P-002). Issuers are named in
  `GATEWAY_GRANT_ISSUERS`.
- **Authorizing a fleet control lives on the gateway, not the fleet engine.** A control
  is enforcement. An endpoint that let the intelligence plane produce one would break
  INV-009 by deployment topology while every type signature still looked right.
- **The console has no write path and never will.** Section 59 forbids it from becoming
  required for execution: the fastest way to stop trading must not run through a
  service the architecture treats as optional.

---

## 4. How it is verified

| Suite | What it needs | What it covers |
|---|---|---|
| `scripts/verify.sh` | nothing | gofmt, vet, unit, structure, security guards, contract, scenarios, console build, Python |
| `make test-integration` | PostgreSQL, ClickHouse, NATS, Redis, SPIRE | Tenant isolation under RLS, idempotency, evidence, retention, SPIRE-issued SVIDs, mTLS submission |
| `make test-chaos` | stops those containers | Enforcement survives ClickHouse, NATS, Redis and the whole intelligence plane going away; PostgreSQL going away fails closed |
| `make test-race` | Docker (this host has no C compiler) | The concurrency guarantees |
| `make test-load` | a running gateway | 1,000 concurrent agents |
| `make test-load-sustained` | a running gateway | Steady traffic, per-minute latency and decision codes |
| `make test-load-tenants` | a running gateway | Several tenants at once |
| `make test-live` | Alpaca Paper credentials | The one check a stub cannot do |

### The structural guards

Ordinary tests check behaviour. These check the *shape* of the system, and they exist
because behaviour tests kept passing over holes:

- **INV-005** — the enforcement packages cannot import the intelligence plane, and
  `internal/identity` cannot import `net/http`. Local enforcement has to survive the
  loss of everything outside the process, and a package that can reach the network is
  one that might.
- **INV-007 route guard** — every `HandleFunc` in the repository is discovered by
  walking the source, and each route that carries tenant data must reach
  `identity.FromTransport`. Discovered rather than listed: the first version named four
  files and a new package was invisible to it.
- **INV-009 plane guard** — the fleet engine's *transitive* import closure may not
  contain `internal/policy`, `internal/authority`, `internal/execution`,
  `internal/broker`, `internal/gateway` or `internal/control`.
- **Route table guard** — the API reference table and the served routes are compared in
  three directions: served-and-undocumented, DONE-and-unserved, served-but-listed-as-
  future.
- **Stale absence guard** — documentation or console prose claiming an endpoint does
  not exist, next to a route that is served, fails the build.
- **OpenAPI guard** — the committed document must equal what the generator produces.
- **Retention guard** — the gateway must actually start the sweeper; a retention policy
  nothing runs is the same as none.

Every guard in this list was verified red before it was trusted: the change it is meant
to catch was made, the guard failed, the change was reverted.

---

## 5. Measured, on the reference machine

A Windows workstation running the dependencies in Docker Desktop. Absolute numbers
describe this laptop; the ratios are what travel.

**Current build.** Measured after the third remediation, against the running binary.

| | Result |
|---|---|
| 1,000 concurrent agents, 3,000 signed submissions | 3,000 accepted, **294/s**, p50 3.6 s end to end over HTTP |
| Sustained, 2 minutes | 29,787 decisions, **248/s**, p50 189 ms p95 236 ms p99 277 ms |
| Multi-tenant, two tenants under load | isolated; neither listed the other's intents |
| Signature verification | p50 **76 µs** (batched; below this platform's clock resolution per call) |
| Authority reservation | p50 **5.5 ms**, p99 7.9 ms |
| Evidence receipt (6 events + outbox rows, one transaction) | p50 **9.8 ms**, p99 13.5 ms |
| Idempotency claim | p50 **3.8 ms**, p99 6.5 ms |
| Evidence outbox | arrival **931/s**, service **1,286/s**, backlog to zero |
| Two gateway processes against one grant | ceiling held; ledger exact |

**The end-to-end throughput fell, and it is not a mystery.** The earlier figures below
were measured before envelope signatures were verified, before authority was reserved
atomically, before the decision receipt was committed ahead of the venue, and before
policy activation was authorized. Every one of those is work now done per submission,
most of it a database round trip on a host where a round trip costs about 4 ms. The
per-stage table above is where the time went.

**Historical, and not comparable.** Kept because deleting a number that fell would be the
more convenient kind of honesty.

| | Result (superseded) |
|---|---|
| Enforcement computation (decode, validate, authority, policy) | p50 12.5 µs, p99 20.5 µs |
| Accepted intent, end to end | 24.6 ms |
| 1,000 concurrent agents | 475/s — no signature verification, no reservation, no receipt |
| Sustained, 2 minutes, 50 workers | 428/s — as above |
| `GET /v1/intents` against 917k events | 5–35 ms |
| Intelligence API against 251k analytical rows | 9–33 ms |
| Cross-replica simulation kill | slot returned in 860 ms |

The single-request latency is dominated by database round trips at roughly 4 ms each on
this host — Docker Desktop on Windows. The enforcement computation itself remains three
orders of magnitude smaller than the round trips around it.

---

## 6. Defect log

The interesting half of this document. Each entry is something that was wrong, how it
was found, and what changed. They are grouped by the shape they share, because after
fifteen audit passes the shape is the finding.

### 6.1 The recurring shape: something true of a part, read as true of the whole

- **A credential proved who was calling and not for whom.** The tenant came from the
  request, so an authenticated caller could name any tenant and every lookup after that
  — grant, policy bundle, idempotency record, the RLS setting itself — obediently used
  it. The database half of INV-007 was enforced and correct the whole time: it isolated
  perfectly to a tenant nobody had established. Proved with a working cross-tenant
  exploit before it was closed.
- **The guards enumerated their own coverage.** A route guard that named four files; a
  plane guard whose forbidden list was hand-maintained and missing `internal/control`.
  Verified by wiring the forbidden package in: the old guard passed.
- **`internal/fleet` said "there is no handler here that writes anything".** True of the
  file, and the fleet engine binary had grown two POST endpoints. The guard is on
  imports now, because that is what bounds a process.
- **The detector wrote evidence nothing reads.** It hand-built one `incident.created.v1`
  whose payload used `cohort_id` where the timeline reads `cohort`, and it never emitted
  `control.recommended.v1` at all — so `POST /v1/controls` answered "this incident
  recommended nothing" for every incident the platform actually detected. Every test
  passed because every test fed the timeline `EventsFor` output, the producer nothing
  in the running system called.
- **Four comments and the API reference said a control's scope was "resolved to concrete
  agents at authorization time".** Nothing resolved anything. It made `ISOLATE_COHORT`
  an action whose name was a lie: the choices were one agent or the whole customer.
- **The console said controls were not persisted, and that simulations had not been
  built.** Both were true when written. A stale caveat is worse than none: it describes
  a system nobody is running and is believed because it sounds careful.

### 6.2 Enforcement defects

- **The venue got the canonical instrument id where a ticker belonged.** The pipeline
  resolved `instr_us_equity_00206R102` to `AAPL` and put it on the order; both adapters
  ignored that and re-resolved through an injected mapping that, in the running gateway,
  was a passthrough. Every real order named an asset no venue has. Found by the first
  live order at Alpaca Paper — every unit test injects a real mapping and the fake
  broker accepts any symbol.
- **One idempotency key with two intents was answered with the wrong order.** The same
  key with a different envelope and a different quantity returned `200`, `DUPLICATE`,
  carrying the earlier order's broker id. The caller was told its seven-share order had
  been accepted and filled; nothing of the kind had been sent.
- **Nothing enforced one-intent-per-envelope** (§12.2). Found when a unique index
  refused to build over twenty-five rows a test suite had created by reusing a fixed id.
- **A refused intent answered `404 no such intent`.** The idempotency record is claimed
  when an order goes to a venue, and identity, authority, controls and policy all refuse
  before that — so the endpoint built to close the caller's loop told anyone who lost a
  403 that their intent never arrived.
- **`POST /v1/controls` shipped with no way to lift a control and no way to see one.**
  The columns and the catalog event existed; nothing wrote them. A tenant-wide
  `READ_ONLY` control could only be lifted with a psql prompt — during exactly the
  incident where that is the worst way to work, and impossible for an operator without
  database access.
- **The application role had no `DELETE`.** The first retention sweep deleted nothing
  and said so in a warning nobody was reading yet.

### 6.3 Measurement defects

- **The performance table measured a part and was read as the whole.** 12.5 µs of
  computation and a 3.34 ms idempotency claim, while an accepted intent took 120 ms.
  The difference was evidence: six events, six transactions, each setting
  `app.tenant_id` to insert one row. Batched into one transaction: **24.6 ms**.
- **`GET /v1/intents` grouped 909,061 rows into 177,087 aggregates to return fifty.**
  Correct from the first day and only wrong at volume; the load runs produced the volume
  to see it in.
- **The `assurance` role used in psql spot checks is a superuser and bypasses row level
  security.** Every isolation check run that way proved nothing. The isolation evidence
  is the Go tests, which connect as `assurance_app`.
- **A benchmark reported p50 = 0s.** Not a fast path: Windows timer granularity is
  coarser than one evaluation, so the percentiles described the clock.
- **The race detector had never run.** When it did, it found nothing — because the
  concurrency tests were missing. Adding them and removing a lock produced four races in
  the usage ledger and five in the in-flight registry. It later caught a real race in a
  test written the same day.

### 6.4 Defects in things built to prevent defects

Worth keeping separate: five of the findings above are in guards, tests or docs written
specifically to catch this class. The lesson recorded in the repository is that a guard
which enumerates its own coverage is a guard that will go blind, and the fix is always
the same — discover the surface instead of listing it.

---

## 7. Decisions taken, with their reasons

- **A decision that cannot be recorded is not acted on.** The receipt — who was
  authenticated, which key signed, which grant allowed, what policy decided, what
  capacity was reserved — is committed before the venue is called. What may still be
  lost afterwards is the outcome, and that is recoverable from the receipt plus
  reconciliation. Neither half is telemetry.
- **Authority limits are a reservation, not a check.** Counting, deciding and writing
  happen in one transaction behind a lock on the grant, immediately before the venue
  call. A definite venue rejection releases the capacity; an ambiguous outcome keeps it.
- **Evidence is never mutated.** A correction is a new event referencing the earlier one
  (ADR-009, INV-006). Both stay in the chain, because a reader needs to see that a
  correction happened rather than a tidied result.
- **Refusals are recorded as carefully as acceptances.** `GET /v1/intents` is built from
  evidence rather than the idempotency table for exactly this reason: that table holds
  what reached a venue, so a list built from it shows what was accepted and silently
  omits every refusal.
- **Unknown is a first-class outcome.** An ambiguous broker timeout produces
  `OUTCOME_UNKNOWN` and reconciliation, never a blind retry (INV-004). `202` means the
  platform does not know yet, and that is not a failure.
- **Fail closed on control state, fail open on telemetry.** An unreadable grant, control
  or idempotency store denies. An unreachable ClickHouse, NATS or fleet engine changes
  nothing about a decision.
- **A stop beats a throttle.** A scope carrying both is told it is stopped rather than
  told it is going too fast.
- **A throttle's rate belongs to the control, not to each agent under it.** Five agents
  at ten per minute get ten between them. Both readings are natural and the difference
  is a factor of five during an incident.
- **Revocation answers 200 twice.** An operator hitting the emergency lever under
  pressure should be told it is done, not handed a 409 to interpret.
- **The platform does not expand a cohort predicate into members.** Membership is
  measured over a rolling window, and an enforcement scope that moved with the
  measurements would be a control nobody authorized. The operator names them.

---

## 8. Deliberate absences

Not gaps. Each one is a decision with a reason:

- **No real-money path.** Both broker adapters refuse any endpoint that is not paper, at
  construction, so a misconfigured URL fails before the first order rather than after.
- **A3 attestation is never produced** (ADR-006).
- **No approval queue.** `REQUIRE_APPROVAL` refuses with a code saying so. An order held
  for an approval nobody can give is an order that silently never happened.
- **No console write path** (§59).
- **No LLM anywhere near a decision** (INV-003, ADR-004).

---

## 9. Open

### 9.1 Retention: answered, and the shape of the answer

The question was circulated and came back with a better answer than the one this
document proposed: **do not encode one universal legal period.** How long a record must
exist depends on the entity, the jurisdiction and the class of record, and a platform
that hard-codes seven years is telling a regulated institution what its obligation is.

What is built is the model rather than the number. Evidence is partitioned by month;
retention is configured per tenant and per record class with hot days, archive days, a
destination and a deletion mode; and four rules sit above any configuration:

- a **legal hold** outranks every policy;
- an **unarchived partition is never destroyed**;
- destruction requires an **approved authorization by someone other than the requester**;
- every **default keeps everything**, including an unknown record class.

A dry-run planner produces verdicts and reasons without touching anything, and archives
carry a hash chain over each event's identity so an edited or truncated archive does not
verify. Destructive purge remains off by default.

The measured arithmetic, for the conversation with a compliance function: **745 bytes
per event, ~5 KB per intention**, so a million intents a day is roughly 1.9 TB a year
uncompressed in PostgreSQL and a few dollars a month archived. Storage is not the
constraint; the accessibility tier is. `docs/EVIDENCE_RETENTION_BRIEF.md` carries the
regulatory reading with sources.

### 9.2 Known coverage gaps, stated rather than discovered later

- The chaos suite drives `execution.Service` directly, so the control stage is not in
  it. That an unreadable control store denies is proved by a unit test that injects the
  failure, not by stopping a container.
- The analytical plane records which control refused an order; no dashboard answers
  "how much did this throttle actually stop". The data is there, the view is not.
- No guard catches prose that describes an existing mechanism *incorrectly* — which is
  where four findings in the thirteenth pass came from. Automating that would mean
  verifying intent, and it is recorded as an open hole rather than pretended away.
- Fleet telemetry still reaches ClickHouse directly rather than through the event
  backbone. Evidence goes through the outbox; intent telemetry does not, and saying
  "everything flows through NATS" would be the same defect the audit found.
- Parent intent is reconstruction and evidence, not an enforcement input. Fragmented
  economic intent is detected and recorded; what stops it is the grant's atomic
  ceiling. Describing it as prevention would overstate it.
- The retention sweep has not been observed on a running binary: this host's
  Application Control began refusing freshly built executables. The store and sweeper
  are covered against real PostgreSQL, and a guard asserts the gateway starts it.

---

## 10. Running it

```sh
make up && make migrate      # containers and schema
./scripts/verify.sh          # the gate: everything that needs no infrastructure
make test-integration        # with the stack up
make test-chaos              # alone: it stops containers
make test-race               # in Docker; this host has no C compiler
```

The gateway serves `POST /v1/intents` only when the enforcement plane is fully
configured — a signed policy bundle, a venue, credentials, an instrument map and a
database. Missing any of them, the route is **absent rather than answering**, and the
process logs which one is missing. A gateway that accepted intents it could not evaluate
would be worse than no gateway.

Credentials are `identity@tenant=token`, minimum 32 characters, compared in constant
time. No secret is ever written to this repository; a test enforces that, and deliberate
test values must end in `_dev_only`.
