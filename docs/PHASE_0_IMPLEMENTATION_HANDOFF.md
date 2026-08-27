# PHASE_0_IMPLEMENTATION_HANDOFF.md

## Agentic Order-Flow Assurance Platform
**Phase:** 0 — Repository + Contracts Foundation  
**Authority:** `MASTER_BUILD_SPEC.md`  
**Execution mode:** STRICT / NO SCOPE EXPANSION

---

# 1. MISSION

Implement **Phase 0 only** of the project defined in `MASTER_BUILD_SPEC.md`.

Do not proceed to Phase 1 or later phases.

The purpose of Phase 0 is to establish a reproducible, reviewable engineering foundation so later agents cannot improvise architecture.

The implementation MUST NOT create:
- trading recommendations;
- AI portfolio logic;
- real-money execution;
- Alpaca order execution;
- fleet-risk algorithms;
- dashboards beyond a minimal boot placeholder if required;
- MCP trading tools;
- microservice sprawl.

---

# 2. FIRST ACTION

Before changing any file:

1. Read `MASTER_BUILD_SPEC.md` in full.
2. Read all existing repository files.
3. Produce a short repository audit:
   - current tree;
   - existing technologies;
   - conflicts with the master spec;
   - files that can remain;
   - files that must be created;
   - files that must NOT be touched.
4. Do not delete existing work without an explicit reason.
5. If the repository is not empty, adapt safely rather than blindly overwriting it.

---

# 3. PHASE 0 DELIVERABLES

Create the monorepo foundation defined by the master spec.

Required logical structure:

```text
agentic-assurance/
│
├── README.md
├── MASTER_BUILD_SPEC.md
├── Makefile
├── docker-compose.yml
├── go.work
├── package.json
├── pnpm-workspace.yaml
├── pyproject.toml
│
├── apps/
│   └── console-web/
│
├── cmd/
│   ├── assurance-gateway/
│   └── fleet-engine/
│
├── internal/
│   ├── gateway/
│   ├── authority/
│   ├── identity/
│   ├── intent/
│   ├── policy/
│   ├── execution/
│   ├── fleet/
│   ├── incident/
│   ├── evidence/
│   └── broker/
│
├── packages/
│   ├── envelope-schema/
│   ├── event-schema/
│   ├── policy-schema/
│   └── telemetry-sdk/
│
├── adapters/
│   ├── alpaca/
│   ├── fakebroker/
│   ├── rest/
│   └── mcp/
│
├── simulator/
│   ├── engine/
│   ├── market/
│   ├── agents/
│   ├── execution/
│   └── assurance/
│
├── scenarios/
│   ├── S01_correlated_stop_loss/
│   ├── S02_poisoned_news/
│   ├── S03_stale_market_feed/
│   ├── S04_model_regression/
│   ├── S05_retry_storm/
│   ├── S06_order_fragmentation/
│   ├── S07_cross_agent_accumulation/
│   ├── S08_liquidity_shock/
│   ├── S09_policy_regression/
│   ├── S10_intelligence_outage/
│   ├── S11_agent_credential_compromise/
│   └── S12_normal_consensus/
│
├── migrations/
│   ├── postgres/
│   └── clickhouse/
│
├── infra/
│   ├── docker/
│   ├── terraform/
│   └── kubernetes/
│
├── tests/
│   ├── integration/
│   ├── security/
│   ├── performance/
│   ├── chaos/
│   └── fixtures/
│
└── docs/
    ├── adr/
    ├── architecture/
    ├── api/
    ├── threat-model/
    ├── operations/
    └── runbooks/
```

Empty directories should contain `.gitkeep` only where the toolchain otherwise cannot preserve them.

Do NOT fill later-phase directories with speculative production code.

---

# 4. TOOLCHAIN FOUNDATION

Configure:

## Go
- Go workspace.
- Modules required for the two Go commands or a clean module organization compatible with the master spec.
- Formatting and static checks.
- Minimal bootable binaries:
  - `assurance-gateway`
  - `fleet-engine`

These binaries should only expose health/startup behavior in Phase 0.

They MUST NOT implement business logic.

## TypeScript / Next.js
- pnpm workspace.
- Next.js application under `apps/console-web`.
- TypeScript strict mode.
- Minimal health/home page identifying the project.
- No financial dashboard implementation yet.

## Python
- `pyproject.toml`.
- Python package foundation for simulator modules.
- Formatting/lint/test configuration.
- One minimal import/boot test.
- No market simulator implementation yet.

---

# 5. LOCAL INFRASTRUCTURE

Create `docker-compose.yml` for development with:

- PostgreSQL
- ClickHouse
- Redis
- NATS JetStream

SPIRE may be added to Docker Compose in Phase 0 only if a minimal configuration can be created without prematurely implementing Phase 2.

Temporal may be included as an optional profile if the setup is clean; it MUST NOT become necessary for Phase 0 boot.

Requirements:
- health checks;
- named volumes;
- localhost-safe development defaults;
- no production secrets;
- documented ports;
- deterministic startup instructions.

Do not add:
- Kafka;
- Neo4j;
- Elasticsearch;
- unnecessary observability stacks.

---

# 6. SCHEMA FOUNDATIONS

Create schema package locations and versioning conventions.

Do NOT fully implement Phase 1 business semantics.

Required:
- versioning policy document;
- placeholder or minimal valid schema scaffolds;
- schema compatibility test harness;
- naming conventions.

Canonical future schemas:
- `AgentExecutionEnvelope`
- internal events
- policy authoring format

The Phase 0 objective is that Phase 1 has a stable place and compatibility mechanism to work in.

---

# 7. ADR FOUNDATION

Create ADR files for all locked decisions from `MASTER_BUILD_SPEC.md`.

At minimum:

```text
ADR-001-product-boundary.md
ADR-002-canonical-intent.md
ADR-003-customer-controlled-enforcement.md
ADR-004-no-llm-hot-path.md
ADR-005-fail-semantics.md
ADR-006-workload-not-model-identity.md
ADR-007-provenance-first-class.md
ADR-008-at-least-once-events.md
ADR-009-append-only-evidence.md
ADR-010-no-graph-db-v0.md
ADR-011-no-premature-microservices.md
ADR-012-broker-adapter-boundary.md
ADR-013-paper-vs-digital-twin.md
ADR-014-no-arbitrary-hri-v0.md
```

Each ADR should include:
- status;
- context;
- decision;
- consequences;
- prohibited reinterpretations.

Do not change the decisions.

---

# 8. ARCHITECTURE DOCUMENTATION

Create:

```text
docs/architecture/system-context.md
docs/architecture/container-view.md
docs/architecture/hot-path.md
docs/architecture/data-ownership.md
```

These must reflect the master spec exactly.

Explicitly document:
- four primary deployables;
- local customer-controlled enforcement;
- asynchronous fleet intelligence;
- no LLM in hot path;
- PostgreSQL vs ClickHouse vs Redis responsibilities;
- MCP as adapter only.

---

# 9. THREAT MODEL FOUNDATION

Create:

```text
docs/threat-model/README.md
```

Include all mandatory threats and security invariants from the master spec.

Do not invent compliance certification.

Security invariants MUST be assigned stable IDs `INV-001` through `INV-015`.

Create the test directory mapping that later phases will use to automate them.

---

# 10. CI FOUNDATION

Configure CI to run, at minimum:

- Go format/test/static checks;
- TypeScript lint/typecheck/test/build;
- Python lint/typecheck/test;
- schema checks;
- repository structure check.

If GitHub Actions is used, keep workflows small and explicit.

Do not add deployment to production.

---

# 11. DEVELOPER COMMANDS

The repository should expose consistent commands.

At minimum, equivalent commands must exist for:

```text
make bootstrap
make up
make down
make test
make lint
make typecheck
make build
make verify
```

`make verify` should represent the Phase 0 quality gate.

Document exact prerequisites.

---

# 12. README

`README.md` must explain:

1. What the platform is.
2. What it is not.
3. Current phase.
4. Architecture summary.
5. Required local dependencies.
6. How to bootstrap.
7. How to run local infrastructure.
8. How to run tests.
9. Where the master specification lives.
10. Explicit warning that real-money execution is not supported.

Do not market the project as an investment assistant.

---

# 13. REPOSITORY HYGIENE

Required:
- `.editorconfig`
- `.gitignore`
- consistent line endings;
- no committed secrets;
- example env file with non-secret development values if necessary;
- dependency lockfiles;
- license placeholder only if ownership/license is known—do not invent a license decision.

No generated binary artifacts in Git.

---

# 14. TESTS REQUIRED IN PHASE 0

At minimum:

## Structure
Verify mandatory directories/files exist.

## Boot
- Go gateway starts and health check works.
- Go fleet-engine starts and health check works.
- Next.js application builds.
- Python simulator package imports.

## Infrastructure
Integration smoke test verifies connectivity to:
- PostgreSQL;
- ClickHouse;
- Redis;
- NATS.

## Isolation from future scope
Add a repository test or documentation guard confirming:
- no real-money broker credentials are required;
- no trading recommendation module exists;
- no HRI implementation exists;
- no LLM package is required by gateway.

---

# 15. PHASE 0 ACCEPTANCE CRITERIA

Phase 0 is complete only if:

- the monorepo boots locally;
- Docker Compose services become healthy;
- Go components compile;
- Next.js builds;
- Python tests pass;
- CI passes;
- `make verify` passes;
- locked ADRs exist;
- system architecture docs exist;
- security invariants are documented;
- no production trading logic exists;
- no real-money execution exists;
- no later-phase business logic has been prematurely implemented.

---

# 16. MANDATORY FINAL REPORT

At completion, respond with exactly these sections:

```text
PHASE
STATUS
REPOSITORY AUDIT
FILES CREATED
FILES MODIFIED
ARCHITECTURE DECISIONS MATERIALIZED
LOCAL INFRASTRUCTURE
TESTS ADDED
TEST RESULTS
BUILD RESULTS
SECURITY CHECKS
KNOWN LIMITATIONS
DEVIATIONS FROM MASTER SPEC
PHASE 1 ENTRY CONDITIONS
```

For `DEVIATIONS FROM MASTER SPEC`:

- write `NONE` if there were none;
- never hide a deviation.

Do not start Phase 1.

---

# 17. STOP CONDITIONS

STOP immediately and report instead of improvising if:

1. `MASTER_BUILD_SPEC.md` is unavailable.
2. Existing repository architecture materially conflicts with a locked ADR.
3. Existing code contains real-money financial execution that would be affected.
4. The requested repository already has unrelated production systems in the same root.
5. A required technology cannot be initialized without changing a locked ADR.
6. A secret or credential appears committed to source control.
7. Phase 0 requires destructive changes not clearly authorized.

A STOP is not failure.

Silent architectural reinterpretation is failure.

---

# END PHASE 0 HANDOFF
