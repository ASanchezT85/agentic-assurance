# Agentic Order-Flow Assurance Platform

**Phase 0 — repository and contracts foundation.**

## What this is

Infrastructure for financial institutions to attest, authorize, monitor, stress-test
and contain AI-generated order flow **before** it becomes operational or market risk.

The platform begins where a structured financial intent already exists. It answers who
generated an action, whether that actor had authority, whether the action violates
deterministic policy, whether it is part of a larger economic intent, and whether
similar actions are emerging across a fleet of agents.

## What this is not

Not a trading bot. Not an investment recommendation engine. Not a robo-advisor. Not a
brokerage. Not a stock picker. Not a generic MCP server. Not a generic AI-governance
dashboard.

It does not answer what to buy, what to sell, when to enter, when to exit, or how to
maximize returns, and it will not be extended to (ADR-001).

> **Real-money execution is not supported and is not implemented.** V0 targets
> FakeBroker and Alpaca Paper only. No live trading path exists in this repository.

## Current phase

Phase 16 of seventeen (0 through 16) — the last.

Phase 0 delivered the monorepo, the locked architecture decisions, schema locations
with a compatibility harness, local infrastructure and CI, with no business logic.

Phase 1 delivered the canonical contract: the `AgentExecutionEnvelope`
(`internal/intent`), its published JSON Schema, deterministic validation, instrument
normalization, and a versioned fixture library.

Phase 2 delivered identity: X509-SVID verification against a trust bundle, the
A0/A1/A2 taxonomy resolved from evidence rather than from what the envelope claims
about itself, and a local SPIRE server that issues the SVIDs the verifier is tested
against.

Phase 3 delivered authority: `AuthorityGrant` with its lifecycle and revocation, the
full section 53 evaluation matrix, and the first persisted tenant-scoped records with
PostgreSQL row level security behind them.

Phase 4 delivered the policy runtime: YAML authoring, a compiler, Ed25519-signed
bundles with content hashes, the staged deployment lifecycle, and a deterministic
evaluator that records the exact bundle in every decision.

Phase 5 delivered the broker lifecycle: the `BrokerAdapter` contract, a fault-injecting
FakeBroker, an Alpaca Paper adapter, reconciliation, and idempotency whose truth lives
in PostgreSQL (ADR-015). An ambiguous timeout never produces a second order.

Phase 6 delivers the event backbone and evidence: the section 32 event catalog, NATS
JetStream publish and consume, an append-only evidence store the database itself
enforces, and the two read-only evidence endpoints of ADR-023. The gateway now serves
`GET /v1/evidence` and `GET /v1/intents/{id}/evidence`, so the chain
`agent → intent → authority → policy → broker order → result` is inspectable with
curl and without the console.

Phase 7 delivers economic intent reconstruction: deterministic clustering of
fragmented orders into a parent intent, principal-level aggregation across agents,
and explainable confidence. Scenarios S06 (order fragmentation) and S07 (cross-agent
accumulation) pass.

Phase 8 delivers fleet telemetry: the ClickHouse schema and batched ingest, rolling
windows, cohorts explained entirely by their predicates, dependency observations that
keep their verification level, and directional imbalance with coverage. Ingest was
measured at roughly 228,000 intents/sec on development hardware, which says ingest is
not the bottleneck and nothing about the end-to-end rate.

Phase 9 delivers the Fleet Risk Vector: eight named components, each carrying its own
value, coverage and sentence of explanation, computed against robust baselines
(median, MAD, quantiles, EWMA) conditioned on instrument, session and hour. **There is
no composite score and there will not be one** (ADR-014). A component that could not
be measured reports UNKNOWN rather than zero, because zero directional imbalance and
unmeasured directional imbalance are opposite findings.

Phase 10 delivers the incident engine: anomaly detection with every finding naming
the rule that produced it, incidents carrying their severity rule and shared
dependencies, human-action audit, and timeline reconstruction **from evidence rather
than from memory**. What the system recommended and what a person did are separate
lines throughout, because conflating them is how a shadow-mode suggestion becomes
indistinguishable from an enforced control.

Phase 11 delivers the Digital Twin: market, agent, execution and assurance engines in
Python, with reproducible experiments. The same seed produces the same experiment, in
the same process and across processes, and the experiment record carries everything
spec section 40 requires to rerun an investigation.

Phase 12 delivers the stress library: all twelve scenarios of spec section 41, each
with assertions, run against the real Go engines. S12 measures the false intervention
rate and forced a genuine change to the detection rules.

Phase 13 delivers shadow mode: the four fleet-level controls as recorded
hypotheticals, a ledger with retrospective precision and false-positive analysis, and
enforcement that the type system reserves for the customer. **All fifteen security
invariants now have tests.**

Phase 14 delivers the console: the six surfaces of spec section 48 and no seventh.
Fleet, Flow, Dependencies and Incidents read real data; Lab and Controls say plainly
what is missing and why, rather than rendering placeholder rows. The console has no
write path, and a test enforces that.

Phase 15 is the hardening gate: all fifteen invariants re-checked for completeness and
under concurrency, a chaos suite that stops real containers, and the section 50.1
targets measured. The enforcement path costs **12.5 µs at p50** against a 2 ms target;
the idempotency round trip costs **3.34 ms** and exceeds that budget on its own, which
ADR-015 predicted and named the remedy for.

Phase 16 closes the build: a behavioural contract suite that all three broker adapters
pass identically, and a second venue adapter whose shapes differ from Alpaca's in ways
that press on the abstraction. Adding it required **zero changes to `internal/`**,
which is the exit criterion.

The submission path is wired. `POST /v1/intents` runs an envelope through every check
in `docs/architecture/hot-path.md`, in that order, and an order reaches a venue only
if all of them allow it. Wiring it produced one finding worth more than the wiring:
`internal/authority` had named a PostgreSQL-backed `UsageSource` as the hot path's
implementation since Phase 3 and there was none. Passing nil makes every grant with a
rolling or daily limit deny with `USAGE_UNAVAILABLE`, which is the correct failure and
a useless system, and nothing noticed because nothing ran the path. The ledger is
built, and a test spends a grant down until each limit actually trips.

The fleet engine is connected the same way, and had the same shape of gap: `Measure`
was called by tests, `InsertMeasurements` by a benchmark, and the intelligence API
read a table nothing wrote. It could answer questions about a fleet it had never
observed, and every answer was an empty list that looks exactly like a calm fleet. The
gateway now feeds the analytical store asynchronously, off the enforcement path, and a
producer measures each closed window. The fleet vector is computed by one
implementation fed from the store, never a second one in SQL, and a round-trip test
fails the moment the stored projection stops carrying a field the measurement reads.

Roadmap: `MASTER_BUILD_SPEC.md` §57.

## Architecture summary

Four deployables (ADR-011, ADR-016):

| Deployable | Language | Plane |
|---|---|---|
| assurance-gateway | Go | **Customer-controlled enforcement** |
| fleet-engine | Go | Intelligence |
| simulation-engine | Python | Offline lab |
| console-web | Next.js | Intelligence |

The load-bearing property is the dependency direction: the enforcement plane depends
on nothing in the intelligence plane. fleet-engine, ClickHouse, NATS, Redis and the
console can all be down while the gateway keeps enforcing hard limits. PostgreSQL is
the one dependency whose loss denies executable intents, and it fails closed.

No LLM sits anywhere in the decision path, synchronous or asynchronous (ADR-004,
ADR-022). Every authorization is reproducible.

Details: `docs/architecture/`.

## Local dependencies

| Tool | Minimum | Tested on |
|---|---|---|
| Go | 1.25 | 1.26.4 |
| Node | 20 | 26.3.0 |
| pnpm | 10 | 11.8.0 |
| Python | 3.12 | 3.14.6 |
| Docker (with compose plugin) | 24 | 29.7.2 |
| make | 4 | optional; see below |

Windows hosts often lack `make`. `scripts/verify.sh` runs the same quality gate.

## Bootstrap

```sh
git clone https://github.com/ASanchezT85/agentic-assurance.git
cd agentic-assurance
make bootstrap          # or: sh scripts/bootstrap.sh
```

`bootstrap` downloads Go modules, installs the pnpm workspace, and creates a project
virtualenv at `.venv` for the Python tools. The venv is not optional convenience: a
global `pip install ruff` lands in the user site-packages, which some environments do
not put on `sys.path`, and the gate then fails in a way that has nothing to do with
your code.

## Local infrastructure

```sh
make up      # postgres, clickhouse, redis, nats — waits for health
docker compose ps
make down    # add -v to docker compose down to drop volumes
```

Ports and credentials: `docs/operations/README.md`. Every port binds to loopback, and
`.env.example` holds development-only, non-secret values. It is the only committed env
file.

## Tests

```sh
make test              # Go, TypeScript, Python unit tests
make test-integration  # infrastructure smoke test; requires `make up`
make verify            # the Phase 0 quality gate: lint, typecheck, test, build
```

`make verify` is what CI runs. It must pass before Phase 1 begins.

If GitHub Actions is unavailable on your account, run the same gate locally on every
push by enabling the shipped hook once per clone:

```sh
git config core.hooksPath scripts/githooks
```

A gate that never runs is decoration. `git push --no-verify` skips it deliberately.

Beyond unit tests, the repository tests itself:

- `tests/structure_test.go` — mandatory directories, root files, all 24 ADRs, all 12
  scenarios, all 15 security invariants documented.
- `tests/scope_guard_test.go` — no broker credentials required, no trading
  recommendation module, no composite risk score, no LLM dependency, exactly six
  console surfaces, and no write path in the console.
- `packages/schema_compat_test.go` — the schema versioning policy, enforced.
- `simulator/test_determinism.py` — the same seed produces the same experiment, in
  one process and across two.
- `internal/intent/schema_sync_test.go` — the published schema and the Go validator
  cannot drift apart in either direction.
- `tests/fixtures/envelopes/` — every invalid fixture declares the error codes it must
  produce, so a fixture that fails for the wrong reason is not a pass.

## Specification

`MASTER_BUILD_SPEC.md` at the repository root is the authoritative specification. It
wins over this README on every point.

`docs/adr/` holds the decisions. ADR-001 through ADR-014 are **locked** by the spec.
ADR-015 through ADR-024 resolve contradictions and omissions found by auditing it;
`docs/adr/README.md` lists every deviation from the spec explicitly, including the one
place where an ADR supersedes it (ADR-018, Temporal).

## License

MIT. Copyright (c) 2026 Alexander J Sanchez T. See `LICENSE`.

## Security

Fifteen security invariants, `INV-001` through `INV-015`, each bound to the phase that
first makes it violable: `docs/threat-model/README.md`. That document also states what
an attacker still gets, because a threat model that lists only wins is marketing.
