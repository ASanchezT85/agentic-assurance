# MASTER_BUILD_SPEC.md

## Agentic Order-Flow Assurance Platform
**Status:** AUTHORITATIVE PRE-BUILD SPECIFICATION  
**Version:** 0.1  
**Date:** 2026-08-27  
**Audience:** Claude Code, Codex, human engineers, reviewers, security reviewers  
**Authority level:** HIGH — implementation MUST conform unless an explicit ADR changes this specification.

---

# 0. DOCUMENT AUTHORITY

This document is the single source of truth for the first production-grade implementation of the Agentic Order-Flow Assurance Platform.

The implementation team MUST NOT reinterpret the product into:
- a trading bot;
- an investment recommendation engine;
- a robo-advisor;
- a brokerage;
- a stock picker;
- a generic MCP server;
- a generic AI-governance dashboard;
- a copy-trading platform;
- a crypto trading product;
- a portfolio-optimization chatbot;
- a “Zesty clone” or “Zesty but better.”

The product category is different.

The platform exists to:

> **Identify, attribute, authorize, observe, simulate, and control AI-generated financial order flow before it reaches execution infrastructure, while preserving customer-controlled enforcement.**

The implementation MUST preserve that boundary.

Any ambiguity MUST be resolved in favor of:
1. deterministic enforcement;
2. traceability;
3. customer control;
4. reproducibility;
5. minimal privilege;
6. explicit uncertainty;
7. infrastructure neutrality;
8. avoiding unnecessary complexity.

---

# 1. PRODUCT THESIS

Financial platforms are increasingly allowing AI agents to interact with brokerage and trading infrastructure. The main technical problem is shifting from:

> “Can an AI agent access a brokerage account?”

to:

> “How does a financial institution safely govern, observe, test, attribute, and contain AI-generated financial actions at scale?”

The platform addresses both:
- **individual agent assurance**, needed today;
- **fleet-level behavioral risk**, expected to become increasingly important as agent populations grow.

The product is designed so that:
- an AI agent can generate a financial intent;
- the institution can cryptographically identify the workload where possible;
- authority is explicitly delegated and bounded;
- deterministic local policies evaluate the action;
- economic intent lineage is reconstructed;
- agent-fleet behavior is observed asynchronously;
- dependencies such as model, strategy, and data feeds are tracked with explicit confidence;
- abnormal consensus and synchronization can be detected;
- the institution retains final enforcement authority;
- historical incidents and simulations are reproducible.

---

# 2. PRODUCT POSITIONING

## 2.1 Working category

**Agentic Order-Flow Assurance**

## 2.2 Working description

> Infrastructure for financial institutions to attest, authorize, monitor, stress-test, and contain AI-generated order flow before it becomes operational or market risk.

## 2.3 What the product does NOT claim

The product MUST NOT claim:
- guaranteed prevention of financial loss;
- guaranteed detection of all malicious agents;
- proof of a model’s private reasoning;
- proof that a declared model generated a specific trade unless provider-side attestation exists;
- causal certainty when only correlation is observed;
- regulatory compliance certification;
- market manipulation detection equivalent to a full exchange surveillance system;
- predictive alpha;
- investment suitability;
- financial advice.

---

# 3. INITIAL CUSTOMER PROFILE

The initial target customer is:

> A broker, wealth-tech platform, fintech, trading platform, or regulated financial institution that allows or plans to allow AI agents to generate or submit financial actions.

The initial product is B2B infrastructure.

It is NOT initially built for:
- retail investors;
- individual traders;
- consumers;
- crypto speculation;
- personal portfolio advice.

---

# 4. PRODUCT BOUNDARY

Our system begins at the point where a structured financial intent exists.

```text
LLM / Agent Reasoning
        |
        | OUTSIDE PRODUCT BOUNDARY
        v
AgentExecutionEnvelope
        |
        | INSIDE PRODUCT BOUNDARY
        v
Identity
Authority
Policy
Lineage
Fleet Intelligence
Execution Assurance
Evidence
        |
        v
Broker / OMS / EMS
```

Our system does not generate trade ideas.

Our system does not answer:
- what stock to buy;
- what asset to sell;
- when to enter;
- when to exit;
- how to maximize returns.

It answers:
- who generated the action;
- whether that actor had authority;
- whether the action violates deterministic policy;
- whether the action is part of a larger economic intent;
- whether similar actions are emerging across a fleet;
- whether dependencies are concentrated;
- whether the behavior deviates materially from baseline;
- what the customer-controlled system should do.

---

# 5. CORE PRINCIPLES

## P-001 — Deterministic enforcement

No LLM may directly determine whether a critical financial action is allowed.

## P-002 — Customer-owned authority

The financial institution retains the final authority over critical enforcement policies.

## P-003 — Provenance is first-class

Every important claim about:
- agent identity;
- model;
- strategy;
- data source;
- authority;
- portfolio context;
- market context

must carry provenance and verification level.

## P-004 — No fake certainty

Unknown provenance remains UNKNOWN.

Declared provenance remains DECLARED.

Verified provenance remains VERIFIED.

The system MUST NOT silently upgrade confidence.

## P-005 — Intent over tool call

The system models economic intent, not merely API requests.

## P-006 — Local safety survives cloud failure

Loss of the intelligence cloud MUST NOT disable customer hard limits.

## P-007 — Audit history is append-only

Historical financial decision evidence is never silently overwritten.

## P-008 — Explain before scoring

The first release uses an explainable Fleet Risk Vector, not an arbitrary composite “AI risk score.”

## P-009 — Simulation before automation

New policies and fleet-risk interventions must support shadow mode and simulation before production enforcement.

## P-010 — Protocol independence

MCP is an adapter, not the core architecture.

---

# 6. LOCKED ARCHITECTURE DECISIONS

## ADR-001 — Product boundary

The platform MUST NOT generate investment recommendations or strategies.

**Status:** LOCKED.

## ADR-002 — Canonical intent model

`AgentExecutionEnvelope` is the canonical contract.

MCP, REST, SDKs, broker-specific APIs, or future protocols must translate into this envelope.

**Status:** LOCKED.

## ADR-003 — Customer-controlled enforcement

Critical market-access and financial-control enforcement must be deployable inside customer-controlled infrastructure.

**Status:** LOCKED.

## ADR-004 — No LLM in hot path

No remote inference, LLM call, or non-deterministic model may sit in the critical synchronous authorization path.

**Status:** LOCKED.

## ADR-005 — Explicit fail semantics

Each subsystem must define fail-open, fail-closed, fail-static, or reconcile-first behavior.

There is no global “fail-open” mode.

**Status:** LOCKED.

## ADR-006 — Workload identity is not model identity

SPIFFE/SPIRE can attest workloads.

It MUST NOT be presented as proof of model reasoning.

**Status:** LOCKED.

## ADR-007 — Provenance metadata is mandatory

Each dependency assertion must include:
- source;
- verification level;
- observation time;
- optional evidence reference.

**Status:** LOCKED.

## ADR-008 — Event delivery is at-least-once

Event consumers MUST be idempotent.

Exactly-once distributed semantics are not a design assumption.

**Status:** LOCKED.

## ADR-009 — Evidence is append-only

Corrections reference prior evidence rather than rewriting it.

**Status:** LOCKED.

## ADR-010 — No graph database in V0

Dependency graph storage uses PostgreSQL plus analytical projections in ClickHouse.

Neo4j or similar graph systems require a future ADR with measured justification.

**Status:** LOCKED.

## ADR-011 — No premature microservices

V0 uses four primary deployables:
1. assurance-gateway;
2. fleet-engine;
3. simulation-engine;
4. console-web.

Service extraction requires measured scaling or ownership justification.

**Status:** LOCKED.

## ADR-012 — Alpaca is an adapter, not the product

The core must not import Alpaca-specific SDK types.

**Status:** LOCKED.

## ADR-013 — Paper trading is not the Digital Twin

Alpaca Paper tests broker lifecycle.

Our simulator tests fleet behavior and market-risk scenarios.

**Status:** LOCKED.

## ADR-014 — No arbitrary HRI score in V0

V0 exposes a Fleet Risk Vector plus coverage/confidence.

A composite HRI requires empirical calibration and a separate ADR.

**Status:** LOCKED.

---

# 7. V0 PHYSICAL ARCHITECTURE

```text
┌─────────────────────────────────────────────┐
│                 AGENT WORLD                 │
│ Claude / GPT / Custom / Rules / Other       │
└─────────────────────┬───────────────────────┘
                      │
            REST / SDK / MCP Adapter
                      │
                      ▼
╔═════════════════════════════════════════════╗
║ CUSTOMER-CONTROLLED ENFORCEMENT PLANE       ║
║                                             ║
║ assurance-gateway — Go                     ║
║                                             ║
║  - envelope validation                     ║
║  - identity verification                   ║
║  - workload attestation                    ║
║  - authority evaluation                    ║
║  - idempotency/replay protection           ║
║  - parent-intent state                     ║
║  - hard policy evaluation                  ║
║  - customer controls                       ║
╚═══════════════════╤═════════════════════════╝
                    │
                    ▼
              BrokerAdapter
                    │
                    ▼
               Alpaca Paper

                    │
              async telemetry
                    ▼

                NATS JetStream
               /              \
              /                \
             ▼                  ▼
      fleet-engine         evidence flow
          Go
           │
     ┌─────┴─────────┐
     ▼               ▼
ClickHouse       PostgreSQL
     │
     ▼
Intelligence API
     │
 ┌───┴────────────┐
 ▼                ▼
console-web    simulation-engine
Next.js        Python
```

---

# 8. REPOSITORY LAYOUT

The initial implementation MUST use a monorepo.

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

Claude/Codex MUST NOT split this into multiple repositories during V0.

---

# 9. TECHNOLOGY STACK

## 9.1 Enforcement and core runtime

**Go**

Use for:
- assurance-gateway;
- authority;
- identity;
- policy execution;
- idempotency;
- broker adapters;
- fleet-engine control logic.

## 9.2 Simulation and quantitative analysis

**Python**

Required baseline libraries:
- NumPy;
- Polars;
- SciPy;
- PyArrow;
- DuckDB.

Pandas may be used only when library compatibility requires it.

## 9.3 Web console

**Next.js + TypeScript**

Use App Router unless a documented incompatibility requires otherwise.

## 9.4 Operational database

**PostgreSQL**

Use for authoritative metadata and control-plane state.

## 9.5 Analytical database

**ClickHouse**

Use for:
- intents;
- orders;
- fills;
- fleet measurements;
- time-window aggregates;
- dependency observations;
- simulation telemetry.

## 9.6 Ephemeral state

**Redis**

Redis is not a source of truth.

Use for:
- hot counters;
- rolling-window cache;
- rate state;
- idempotency cache;
- short-lived local coordination.

## 9.7 Events

**NATS JetStream**

Do not introduce Kafka in V0.

## 9.8 Workflow

**Temporal**

Temporal MUST NOT be on the critical pre-trade hot path.

Use it for:
- approvals;
- reconciliation workflows;
- policy rollout orchestration;
- long-running simulations;
- incident workflows.

## 9.9 Identity

**SPIFFE/SPIRE**

Used for workload identity and attestation.

## 9.10 Observability

**OpenTelemetry**

Logs are not audit evidence.

## 9.11 Local development

**Docker Compose**

Kubernetes is not required for normal local development.

## 9.12 Production deployment target

**Kubernetes**

Only after local functional correctness is established.

---

# 10. BOUNDED CONTEXTS

The implementation must preserve the following domain boundaries.

## 10.1 Identity

Entities:
- Agent;
- WorkloadIdentity;
- Attestation;
- VerificationLevel.

## 10.2 Authority

Entities:
- Principal;
- AuthorityGrant;
- Delegation;
- Revocation.

## 10.3 Intent

Entities:
- AgentExecutionEnvelope;
- ParentIntent;
- IntentLineage;
- NormalizedInstrument.

## 10.4 Policy

Entities:
- PolicyBundle;
- PolicyRule;
- PolicyDecision;
- ControlAction.

## 10.5 Execution

Entities:
- BrokerOrder;
- ExecutionState;
- Fill;
- ReconciliationRecord.

## 10.6 Fleet

Entities:
- Cohort;
- RiskVector;
- Baseline;
- DependencyObservation;
- FleetMetric.

## 10.7 Incident

Entities:
- Incident;
- IncidentEvent;
- Evidence;
- Timeline;
- HumanAction.

## 10.8 Simulation

Entities:
- Scenario;
- AgentArchetype;
- AgentPopulation;
- MarketReplay;
- Experiment;
- ExperimentResult.

These are bounded contexts, not mandatory standalone services.

---

# 11. VERIFICATION LEVELS

Use exactly the following V0 verification taxonomy.

```text
UNKNOWN
DECLARED
VERIFIED
```

For agent identity, optionally expose an attestation level:

```text
A0 = unknown origin
A1 = authenticated app/API identity
A2 = workload-attested identity
A3 = provider-attested runtime/model identity
```

V0 is expected to reliably support A2 for controlled workloads.

A3 MUST NOT be simulated or falsely claimed.

---

# 12. AGENT EXECUTION ENVELOPE

## 12.1 Purpose

The `AgentExecutionEnvelope` is the canonical financial-intent object.

All inbound adapters must normalize into this schema before policy execution.

## 12.2 Required conceptual fields

```json
{
  "schema_version": "0.1",
  "envelope_id": "env_...",
  "idempotency_key": "idem_...",
  "correlation_id": "corr_...",
  "received_at": "RFC3339 timestamp",

  "tenant_id": "tenant_...",

  "principal": {
    "principal_id": "principal_...",
    "account_id": "account_..."
  },

  "agent": {
    "agent_id": "agent_...",
    "workload_identity": {
      "spiffe_id": "spiffe://..."
    },
    "attestation": {
      "level": "A2",
      "method": "SPIFFE_X509_SVID",
      "evidence_ref": "att_..."
    }
  },

  "runtime_claims": {
    "model_provider": {
      "value": "provider-x",
      "verification": "DECLARED"
    },
    "model_family": {
      "value": "model-y",
      "verification": "DECLARED"
    },
    "model_version": {
      "value": "2026-08",
      "verification": "DECLARED"
    }
  },

  "authority_grant_id": "grant_...",

  "dependencies": [
    {
      "type": "MARKET_DATA",
      "id": "feed-a",
      "verification": "VERIFIED",
      "observed_at": "RFC3339 timestamp"
    }
  ],

  "intent": {
    "asset_class": "EQUITY",
    "instrument_id": "instr_...",
    "side": "BUY",
    "order_type": "MARKET",
    "notional": 4200,
    "quantity": null,
    "limit_price": null,
    "stop_price": null,
    "time_in_force": "DAY",
    "extended_hours": false
  },

  "lineage": {
    "parent_intent_id": null,
    "strategy_id": "strategy_..."
  },

  "context": {
    "portfolio_snapshot_id": "ps_...",
    "market_snapshot_id": "ms_..."
  },

  "signature": {
    "algorithm": "implementation-defined",
    "value": "..."
  }
}
```

## 12.3 Invariants

- `quantity XOR notional` — exactly one may be the primary sizing field.
- `tenant_id` is mandatory.
- `authority_grant_id` is mandatory for executable intents.
- executable intents require an authenticated agent identity.
- unknown model provenance is permitted but must remain UNKNOWN.
- timestamps must be normalized to UTC.
- all identifiers must be immutable once issued.

---

# 13. INSTRUMENT IDENTITY

Ticker symbols are not canonical identifiers.

The internal system MUST normalize to `instrument_id`.

Metadata MAY include:
- symbol;
- venue;
- FIGI;
- ISIN;
- CUSIP where legally/operationally appropriate;
- currency;
- asset class.

Policies operate on normalized instrument identity.

---

# 14. AUTHORITY GRANT

## 14.1 Purpose

An `AuthorityGrant` defines what an agent may do.

Natural-language prompts MUST NOT serve as enforceable authority.

## 14.2 Required fields

```json
{
  "grant_id": "grant_...",
  "tenant_id": "tenant_...",
  "principal_id": "principal_...",
  "agent_id": "agent_...",

  "issued_at": "RFC3339",
  "valid_from": "RFC3339",
  "valid_until": "RFC3339",

  "allowed_operations": ["BUY", "SELL"],
  "allowed_asset_classes": ["EQUITY", "ETF"],

  "allowed_instruments": [],
  "denied_instruments": [],

  "limits": {
    "per_order_notional": 5000,
    "rolling_1h_notional": 10000,
    "daily_notional": 15000,
    "max_open_orders": 10
  },

  "status": "ACTIVE"
}
```

> **V0 divergence (ADR-026).** This record used to carry
> `capabilities.margin_allowed` and `capabilities.shorting_allowed`. Neither was ever
> enforced — deciding whether a SELL is a short needs position data the platform does
> not hold — so a grant could state `shorting_allowed: false` while the platform
> authorized the short. `POST /v1/authority-grants` now refuses a request carrying
> either field rather than accepting a control it does not apply. They return, with
> enforcement written first, when there is a position model behind them.

## 14.3 Mandatory decisions

The gateway MUST deny:
- expired grants;
- revoked grants;
- future grants;
- wrong-agent use;
- wrong-principal use;
- wrong-account use;
- disallowed operations;
- limits exceeded.

---

# 15. POLICY MODEL

## 15.1 Authoring format

V0 policy authoring MAY use YAML.

Example:

```yaml
version: 1
policy: retail_agent_standard

rules:
  - id: ORDER_MAX_NOTIONAL
    when:
      asset_class: EQUITY
    require:
      notional_lte: 5000
    action: DENY

  - id: OPTIONS_DISABLED
    when:
      asset_class: OPTION
    action: DENY

  - id: LARGE_ORDER_APPROVAL
    when:
      notional_gt: 2500
    action: REQUIRE_APPROVAL
```

## 15.2 Production execution

YAML MUST NOT be interpreted dynamically on every order.

Policy lifecycle:

```text
DRAFT
  ↓
VALIDATE
  ↓
COMPILE
  ↓
SIGN
  ↓
SIMULATE
  ↓
SHADOW
  ↓
CANARY
  ↓
ACTIVE
```

Each policy bundle must include:
- `bundle_id`;
- version;
- content hash;
- signature;
- activation metadata.

Every decision must record the exact bundle used.

---

# 16. CONTROL ACTIONS

V0 may emit the following actions:

```text
ALLOW
DENY
OBSERVE
REQUIRE_APPROVAL
THROTTLE
DELAY
ISOLATE_COHORT
READ_ONLY
```

Rules:
- hard identity or authority failures -> DENY;
- fleet analytics in early MVP -> recommendation/shadow by default;
- critical automatic fleet containment requires explicit customer policy;
- control actions must be auditable.

---

# 17. FAIL SEMANTICS

The implementation MUST satisfy this table.

| Condition | Required behavior |
|---|---|
| Invalid identity | DENY |
| Invalid signature | DENY |
| Expired grant | DENY |
| Revoked grant | DENY |
| Invalid envelope | DENY |
| Hard policy unavailable | DENY |
| Exact duplicate request | Return deterministic prior outcome |
| Intelligence cloud unavailable | Continue local hard enforcement |
| Fleet engine unavailable | Continue local hard enforcement |
| Telemetry unavailable | Buffer locally within configured bounds |
| Broker timeout | Mark UNKNOWN and reconcile |
| Market data stale | Apply explicit policy; never silently assume current |
| Risk vector low confidence | Do not auto-block by default |
| Console unavailable | Production execution unaffected |
| Simulator unavailable | Production execution unaffected |

Blind retries after broker timeout are prohibited.

---

# 18. BROKER ADAPTER CONTRACT

The core MUST depend on an abstraction.

Conceptual interface:

```text
Capabilities()
SubmitOrder()
CancelOrder()
GetOrder()
GetOrders()
GetPositions()
GetAccount()
Reconcile()
```

Required initial adapters:
1. `FakeBrokerAdapter`
2. `AlpacaPaperAdapter`

The core MUST NOT import Alpaca-specific types.

A second non-Alpaca adapter or contract test implementation must be created before architecture is considered stable.

---

# 19. IDEMPOTENCY AND REPLAY PROTECTION

Each executable intent requires:
- unique `envelope_id`;
- `idempotency_key`;
- tenant scope;
- bounded retention;
- deterministic duplicate handling.

A duplicate must not cause a second broker execution.

Retry flow after ambiguous network state:

```text
submit
  ↓
timeout
  ↓
UNKNOWN
  ↓
reconcile with broker
  ↓
resolved?
 ├─ yes -> return result
 └─ no  -> remain UNKNOWN / operator flow
```

---

# 20. PARENT INTENT ENGINE

## 20.1 Goal

Detect a larger economic intent that is fragmented across multiple tool calls or agents.

## 20.2 V0 method

Deterministic clustering only.

Signals:
- same tenant;
- same principal;
- same or equivalent instrument;
- same side;
- short temporal window;
- same or related agent;
- same strategy;
- same market context;
- same authority grant or related grant.

Output:

```json
{
  "parent_intent_id": "pi_...",
  "instrument_id": "instr_...",
  "side": "BUY",
  "child_count": 14,
  "agent_count": 3,
  "gross_notional": 48200,
  "time_span_ms": 11800,
  "confidence": 0.87
}
```

Do not claim causality.

This is a reconstruction with confidence.

---

# 21. CROSS-AGENT ACCUMULATION

Policies MAY operate on:
- individual order;
- individual agent;
- principal;
- account;
- parent economic intent;
- cohort.

Example:

```text
Agent A BUY 4,000
Agent B BUY 4,500
Agent C BUY 3,000
```

For the same principal/instrument/window:

```text
effective exposure change = +11,500
```

This may trigger a principal-level rule even if every individual action passes.

---

# 22. FLEET RISK VECTOR V0

No composite HRI in V0.

The risk vector is:

\[
R = (D, B, C_m, C_s, C_f, P, A, Q)
\]

Where:

- \(D\): directional imbalance
- \(B\): temporal burst
- \(C_m\): model concentration
- \(C_s\): strategy concentration
- \(C_f\): feed concentration
- \(P\): projected market participation
- \(A\): abnormal consensus
- \(Q\): quality/coverage/confidence metadata

---

# 23. DIRECTIONAL IMBALANCE

For intent set \(i\):

\[
D =
\frac{\left|\sum_i s_i n_i\right|}
{\sum_i n_i}
\]

where:
- \(s_i = +1\) for BUY;
- \(s_i = -1\) for SELL;
- \(n_i\) is normalized notional.

Interpretation:

```text
0.0 -> balanced
1.0 -> fully one-directional
```

The system must preserve both:
- gross flow;
- net flow.

---

# 24. TEMPORAL BURST

Do not use a fixed global threshold.

Baseline by:
- instrument;
- market session;
- time of day;
- volatility regime;
- event regime;
- liquidity regime.

V0 output should include:
- observed intents/sec;
- baseline median;
- MAD-based deviation;
- z-like robust score;
- historical percentile.

Prefer robust statistics:
- median;
- MAD;
- quantiles;
- EWMA.

---

# 25. DEPENDENCY CONCENTRATION

Use HHI-style concentration where appropriate.

For shares \(p_j\):

\[
HHI = \sum_j p_j^2
\]

Calculate separately for:
- model family;
- strategy;
- market-data source;
- news source;
- cloud/runtime provider when available;
- execution adapter.

Every concentration result MUST include:
- observed coverage;
- verified coverage;
- declared coverage;
- unknown coverage.

Example:

```text
Observed model concentration: 0.73
Coverage: 0.80
Verified coverage: 0.20
Confidence: LOW-MEDIUM
```

---

# 26. ABNORMAL CONSENSUS

V0 MUST distinguish:
- consensus;
- abnormal consensus.

Conceptual form:

\[
AC =
ObservedConsensus -
ExpectedConsensus(Context)
\]

Context includes:
- instrument;
- event severity;
- volatility;
- liquidity;
- time of day;
- market regime;
- known market news where available.

V0 implementation may use a robust statistical baseline rather than ML.

---

# 27. FEEDBACK COUPLING

V0 may compute correlation indicators between:
- price movement;
- spread;
- depth;
- volatility;
- subsequent same-direction agent flow.

The product MUST NOT claim causal proof.

This feature is informational in V0.

---

# 28. QUALITY AND CONFIDENCE

Confidence is not optional.

Every fleet-risk view must surface:
- data coverage;
- verification coverage;
- unknown provenance;
- stale-data state;
- model uncertainty if applicable.

The UI MUST NOT collapse low-confidence data into a precise high-confidence score.

---

# 29. FLEET ENGINE

Responsibilities:
- streaming aggregate state;
- rolling windows;
- cohort construction;
- dependency concentration;
- risk-vector computation;
- baseline comparison;
- anomaly event generation;
- incident candidate generation.

The fleet engine MUST NOT:
- submit broker orders;
- modify customer hard policy directly;
- call LLMs synchronously;
- become required for identity/authority enforcement.

---

# 30. COHORT MODEL

A cohort is a dynamic or static group of intents/agents sharing one or more properties.

Examples:
- same instrument;
- same side;
- same strategy;
- same model family;
- same feed;
- same tenant;
- same principal;
- same anomaly signature.

Every cohort must be explainable by explicit predicates.

No opaque ML-only cohort identifier is acceptable in V0.

---

# 31. EVIDENCE MODEL

Audit evidence is append-only.

Each evidence event must contain:
- event id;
- tenant id;
- actor;
- action;
- object;
- prior object reference where applicable;
- timestamp;
- correlation id;
- causation id;
- schema version;
- producer;
- content hash/signature where applicable.

Corrections reference earlier evidence instead of mutating it.

---

# 32. INTERNAL EVENT CATALOG

Initial events:

```text
agent.intent.received.v1
agent.identity.verified.v1
agent.identity.failed.v1
authority.evaluated.v1
policy.evaluated.v1
intent.parent.linked.v1

broker.order.submitted.v1
broker.order.unknown.v1
broker.order.accepted.v1
broker.order.rejected.v1
broker.order.filled.v1
broker.order.cancelled.v1
broker.order.reconciled.v1

fleet.metric.updated.v1
fleet.cohort.created.v1
fleet.anomaly.detected.v1

incident.created.v1
incident.updated.v1
incident.escalated.v1
incident.closed.v1

control.recommended.v1
control.applied.v1
control.revoked.v1

policy.bundle.created.v1
policy.bundle.activated.v1
policy.bundle.rolled_back.v1

simulation.started.v1
simulation.completed.v1
simulation.failed.v1
```

Every event must include:

```text
event_id
tenant_id
aggregate_id
correlation_id
causation_id
schema_version
occurred_at
produced_at
producer
sequence
payload
```

---

# 33. DATABASE RESPONSIBILITIES

## 33.1 PostgreSQL — source of truth

Tables/domain groups:
- tenants;
- users;
- principals;
- accounts;
- agents;
- workload identities;
- attestations;
- authority grants;
- authority revocations;
- policy bundles;
- policy deployment records;
- instruments;
- broker connections;
- incidents;
- incident actions;
- control actions;
- simulation definitions;
- scenario metadata;
- experiment metadata.

## 33.2 ClickHouse — analytical telemetry

Tables/domain groups:
- intents;
- broker orders;
- fills;
- fleet measurements;
- rolling windows;
- dependency observations;
- cohort observations;
- anomaly features;
- simulation telemetry.

## 33.3 Redis — ephemeral only

Use for:
- rolling hot state;
- idempotency cache;
- counters;
- bounded buffering;
- rate-limiting state.

Redis is never the canonical source of:
- authority;
- policy;
- incident history;
- execution truth.

---

# 34. MULTI-TENANCY

All domain objects require `tenant_id`.

Minimum V0 isolation:
- PostgreSQL Row Level Security;
- tenant context in application layer;
- tenant-scoped credentials;
- tenant-scoped object storage paths;
- tenant-scoped encryption keys where appropriate.

Cross-tenant analytics are prohibited in the normal application path.

Any future aggregate learning requires a separate privacy/data-governance ADR.

---

# 35. SECRETS

Broker secrets MUST NOT be stored in plaintext.

Production target:
- customer secret manager;
- KMS;
- Vault-compatible system.

Short-lived credentials should be preferred when supported.

Secrets must never be:
- logged;
- returned through APIs;
- embedded in evidence;
- stored in telemetry payloads.

---

# 36. HUMAN ACTION AUDIT

The system must audit humans as well as agents.

Audit:
- policy changes;
- grant creation;
- grant revocation;
- cohort throttle;
- halt;
- resume;
- threshold change;
- incident acknowledgment;
- incident closure;
- emergency override.

A human operator may create operational risk; human actions are therefore part of the evidence model.

---

# 37. DIGITAL TWIN

The Digital Twin is independent from broker paper trading.

## 37.1 Purpose

Test:
- agent-fleet behavior;
- fleet concentration;
- false-positive handling;
- policy effects;
- failure modes;
- market-impact approximations;
- customer-control behavior.

## 37.2 Engines

```text
Market Engine
Agent Engine
Execution Engine
Assurance Engine
```

## 37.3 Flow

```text
Historical / synthetic market
          ↓
      Market Engine
          ↓
    Agent Population
          ↓
     Intent Stream
          ↓
   Assurance Engine
          ↓
   Execution Model
          ↓
 Synthetic Market Response
          ↺ feedback
```

---

# 38. MARKET ENGINE V0

V0 does not reproduce a full exchange matching engine.

Minimum state:
- mid price;
- best bid/ask;
- spread;
- volume;
- depth approximation;
- volatility;
- temporary impact;
- permanent impact approximation.

A configurable square-root-style impact approximation MAY be used for stress testing:

\[
Impact \propto \sigma \sqrt{\frac{Q}{V}}
\]

This must be documented as an approximation, not market truth.

---

# 39. AGENT POPULATION MODEL

Synthetic agents MUST be reproducible.

Each archetype may define:
- population count;
- capital distribution;
- strategy;
- declared model dependency;
- declared feed dependency;
- reaction-latency distribution;
- risk thresholds;
- position limits;
- error profile;
- panic probability;
- retry behavior;
- stale-data sensitivity.

Example:

```yaml
archetype: momentum_conservative

population: 1200

capital:
  distribution: lognormal

latency_ms:
  distribution: lognormal
  median: 1800

dependencies:
  model: model_A
  news_feed: feed_B

risk:
  max_position_pct: 0.08

behavior:
  panic_probability: 0.03
```

Random seed must be explicit.

Same:
- scenario;
- code version;
- policy bundle;
- dataset;
- seed

must produce the same experiment outcome within deterministic numerical tolerance.

---

# 40. SIMULATION EXPERIMENT RECORD

Every simulation must store:

```text
experiment_id
scenario_id
scenario_version
code_commit
random_seed
policy_bundle_id
population_definition_hash
market_dataset_id
market_dataset_hash
started_at
completed_at
results
```

This enables reproducible investigations.

---

# 41. REQUIRED STRESS SCENARIOS

The initial library MUST contain all twelve scenarios.

## S01 — Correlated stop-loss
A fast market decline triggers similar risk thresholds.

Expected:
- directional burst detected;
- synchronization detected;
- no false attribution of malicious intent.

## S02 — Poisoned news
A shared news dependency receives false information.

Expected:
- source concentration detected;
- abnormal consensus rises;
- incident includes dependency evidence.

## S03 — Stale market feed
Agents operate from stale pricing.

Expected:
- stale-data state visible;
- policy can deny or require approval.

## S04 — Model regression
A simulated model-family behavior changes after upgrade.

Expected:
- cohort behavior deviation detected;
- dependency graph identifies common model declaration.

## S05 — Retry storm
Network timeouts cause repeated submissions.

Expected:
- idempotency prevents duplicate broker execution;
- unknown execution state reconciles before retry.

## S06 — Order fragmentation
An agent splits a large intent into smaller requests.

Expected:
- parent intent reconstructed;
- effective notional exceeds rule;
- no reliance solely on per-request limit.

## S07 — Cross-agent accumulation
Multiple agents under one principal accumulate exposure.

Expected:
- principal-level aggregation catches combined exposure.

## S08 — Liquidity shock
Available depth deteriorates while order flow continues.

Expected:
- participation/liquidity metrics deteriorate;
- shadow recommendation generated.

## S09 — Policy regression
A new policy configuration introduces unsafe behavior.

Expected:
- pre-production simulation surfaces impact;
- staged rollout prevents silent global activation.

## S10 — Intelligence outage
Cloud intelligence disappears during high activity.

Expected:
- hard local enforcement continues;
- telemetry buffers within bounds;
- production does not depend on cloud intelligence.

## S11 — Agent credential compromise
Unauthorized workload attempts execution.

Expected:
- identity/attestation failure;
- DENY;
- security incident evidence.

## S12 — Normal consensus
A legitimate material event causes broad rational agreement.

Expected:
- consensus may be high;
- system does not automatically label it abnormal;
- false intervention rate is measured.

---

# 42. SHADOW MODE

Fleet-level controls begin in shadow mode.

In shadow mode:
- actual local hard policies still enforce;
- fleet intelligence may produce hypothetical actions;
- no autonomous fleet blocking unless explicitly enabled.

Record:

```text
would_have_denied
would_have_throttled
would_have_required_approval
would_have_isolated
```

Shadow mode must support retrospective precision/false-positive analysis.

---

# 43. POLICY ROLLOUT

Required lifecycle:

```text
DRAFT
  ↓
VALIDATE
  ↓
SIMULATE
  ↓
SHADOW
  ↓
CANARY
  ↓
ACTIVE
```

Rollback must be explicit and audited.

Production policy must never be edited in place.

Every change creates a new version.

---

# 44. SECURITY INVARIANTS

These are mandatory and must become automated tests.

## INV-001
An unauthenticated workload can never create an executable order.

## INV-002
An agent can never exercise more authority than its active grant.

## INV-003
No LLM output can bypass deterministic policy.

## INV-004
No ambiguous broker timeout may trigger blind duplicate execution.

## INV-005
Loss of the intelligence cloud cannot disable local hard limits.

## INV-006
Historical evidence cannot be silently mutated.

## INV-007
Tenant A cannot observe Tenant B data.

## INV-008
Unknown provenance can never be represented as verified provenance.

## INV-009
Fleet intelligence may recommend; customer policy authorizes enforcement.

## INV-010
A new policy cannot reach production without versioning and validation.

## INV-011
Redis loss cannot destroy authoritative financial-control state.

## INV-012
A broker adapter failure cannot corrupt the canonical core domain model.

## INV-013
Audit logs and application logs are not interchangeable.

## INV-014
Model identity must never be inferred from workload identity without evidence.

## INV-015
An invalid instrument normalization result cannot proceed to executable policy.

---

# 45. THREAT MODEL

Minimum threats and controls:

| Threat | Mandatory defense |
|---|---|
| Stolen credential | workload identity + revocation + short-lived credentials |
| Forged agent identity | SVID validation |
| Replay attack | nonce/idempotency |
| Retry storm | deterministic duplicate handling + reconciliation |
| Prompt injection | authority boundary + policy boundary |
| Malicious agent | hard execution envelope |
| Compromised feed | dependency concentration + provenance |
| Model regression | cohort comparison + version metadata |
| Strategy cloning | strategy concentration |
| Model concentration | dependency telemetry |
| Policy misconfiguration | staged deployment + simulation |
| Cloud outage | local enforcement |
| Simulator error | deterministic replay + versioned datasets |
| Telemetry poisoning | signed provenance where available |
| Cross-tenant leakage | RLS + tenant-scoped credentials |
| Secret leakage | KMS/secret-manager discipline |
| Human operator error | human-action audit |
| Fragmented order evasion | parent-intent reconstruction |
| Cross-agent exposure accumulation | principal-level aggregation |

---

# 46. PUBLIC API V0

Initial public surface should remain small.

```text
POST /v1/intents
GET  /v1/intents/{id}

POST /v1/authority-grants
POST /v1/authority-grants/{id}/revoke

GET  /v1/fleet/state
GET  /v1/cohorts

GET  /v1/incidents
GET  /v1/incidents/{id}

POST /v1/controls

POST /v1/simulations
GET  /v1/simulations/{id}
```

All mutation endpoints require:
- authenticated tenant;
- authorized actor;
- correlation id;
- audit event.

---

# 47. MCP ADAPTER V0

MCP is optional until REST and core contracts are stable.

When implemented, initial tools should be:

```text
get_agent_authority
submit_financial_intent
get_intent_status
get_portfolio
cancel_intent
```

Do not expose core architecture as:
- `buy_stock`;
- `sell_stock`;
- `trade_crypto`.

The agent submits intent; the platform owns assurance.

---

# 48. CONSOLE SURFACES

V0 has exactly six principal surfaces.

## 48.1 Fleet
Shows:
- connected agents;
- attestation coverage;
- intent rate;
- major directional flow;
- risk-vector summary;
- abnormal cohorts.

## 48.2 Flow
Shows:
- active intents;
- instrument;
- side;
- notional;
- agent;
- principal;
- authority result;
- policy result.

## 48.3 Dependencies
Shows:
- model concentration;
- strategy concentration;
- feed concentration;
- provenance coverage;
- dependency relationships.

## 48.4 Incidents
Shows:
- incident severity;
- evidence timeline;
- cohort;
- dependency graph;
- proposed/actual controls;
- human actions.

## 48.5 Lab
Shows:
- scenario;
- population;
- market replay;
- policy bundle;
- experiment result;
- compare runs.

## 48.6 Controls
Shows:
- active policy bundle;
- grant state;
- throttle;
- cohort isolation;
- read-only mode;
- kill switch;
- audit history.

Do not add extra dashboards in V0 unless they satisfy a defined acceptance requirement.

---

# 49. INCIDENT TIMELINE

Every incident must be reconstructable.

Example structure:

```text
14:32:04.182 directional imbalance deviated from baseline
14:32:04.901 intent rate +4.7 MAD-equivalent deviation
14:32:05.023 strategy concentration increased
14:32:05.121 feed concentration increased
14:32:05.419 cohort reached 2,184 agents
14:32:05.700 recommendation = THROTTLE
14:32:06.018 shadow mode — no production intervention
```

The system must be able to explain:
- what happened;
- when;
- which agents/principals were involved;
- which dependencies were shared;
- what evidence supported the conclusion;
- what the system recommended;
- what the customer actually did.

---

# 50. PERFORMANCE TARGETS

These are engineering targets, not external SLAs.

## 50.1 Gateway overhead

Target:
```text
p50 < 2 ms
p95 < 5 ms
p99 < 10 ms
```

excluding external network/broker latency.

## 50.2 Fleet anomaly latency

Target:
```text
significant fleet anomaly surfaced < 1 second
```

at MVP load target.

## 50.3 Capacity target

MVP benchmark target:
```text
10,000 connected simulated agents
10,000 intents/sec sustained
25,000 intents/sec burst
10M+ replayed events
100+ concurrent cohorts
12 reproducible stress scenarios
```

No architecture decision should claim Nasdaq-scale capacity.

---

# 51. OBSERVABILITY

OpenTelemetry is required.

Capture:
- traces;
- metrics;
- operational logs.

Rules:
- operational logs are not audit evidence;
- secrets never enter logs;
- correlation id must propagate;
- tenant id must be available for authorized diagnostics without accidental cross-tenant mixing.

---

# 52. TEST STRATEGY

Every major feature requires tests in four dimensions:

```text
FUNCTIONAL
SECURITY
PERFORMANCE
FAILURE
```

No feature is complete with only happy-path tests.

---

# 53. AUTHORITY TEST MATRIX

Minimum cases:

| Case | Expected |
|---|---|
| Valid grant | ALLOW if other policy passes |
| Expired grant | DENY |
| Future grant | DENY |
| Revoked grant | DENY |
| Wrong agent | DENY |
| Wrong principal | DENY |
| Wrong account | DENY |
| Disallowed operation | DENY |
| Exceeds per-order limit | DENY |
| Exceeds rolling limit | DENY |
| Exceeds daily limit | DENY |
| Invalid signature | DENY |
| Replay | deterministic duplicate behavior |
| Local policy cloud disconnected | enforcement still works |

---

# 54. BROKER FAILURE TEST MATRIX

Minimum cases:
- request accepted;
- request rejected;
- request timeout before broker receives it;
- request timeout after broker receives it;
- duplicate network retry;
- partial fill;
- fill after local timeout;
- cancellation race;
- stale broker status;
- reconciliation resolves unknown order;
- reconciliation cannot resolve.

No blind retry is allowed for ambiguous state.

---

# 55. CHAOS TESTS

Required:
- stop fleet-engine;
- stop ClickHouse;
- stop PostgreSQL read replica if used;
- stop Redis;
- stop NATS;
- isolate intelligence cloud;
- inject network latency;
- kill console;
- restart gateway;
- broker API timeout;
- policy bundle unavailable.

Expected principle:

> Critical identity, authority, and local hard limits remain deterministic or fail closed as specified.

---

# 56. DEFINITION OF DONE — MVP

MVP is NOT complete until all conditions below are demonstrated.

1. 1,000+ synthetic agents can send concurrent intents.
2. Controlled workloads can reach attestation level A2.
3. `AgentExecutionEnvelope` is versioned and validated.
4. Authority Grants are deterministic and enforceable.
5. Policy bundles are compiled, signed, versioned, and staged.
6. Idempotency prevents duplicate execution.
7. Ambiguous broker timeout uses reconciliation.
8. Parent Intent reconstruction works on fragmented orders.
9. Cross-agent principal aggregation works.
10. Individual hard policy enforcement works without cloud intelligence.
11. Fleet telemetry streams asynchronously.
12. Directional imbalance is computed.
13. Temporal burst is computed against robust baselines.
14. Model/feed/strategy concentration is computed with coverage.
15. Fleet Risk Vector is visible without arbitrary composite score.
16. Provenance confidence is explicit.
17. Dependency relationships are queryable.
18. Shadow mode records hypothetical controls.
19. Incident timeline is reproducible.
20. Alpaca Paper adapter works end-to-end.
21. FakeBroker adapter works end-to-end.
22. Digital Twin runs independently of Alpaca.
23. All twelve stress scenarios are reproducible.
24. S12 Normal Consensus demonstrates non-overreaction.
25. Local enforcement survives intelligence-cloud outage.
26. Evidence is append-only.
27. Tenant isolation tests pass.
28. Gateway performance benchmark is reproducible.
29. No real-money trading is enabled.
30. Documentation permits a second broker adapter without core changes.

---

# 57. BUILD PHASES

Implementation MUST proceed in these phases.

## Phase 0 — Repository and contracts

Deliver:
- monorepo;
- ADR directory;
- schemas;
- linting;
- formatting;
- test harness;
- Docker Compose;
- CI skeleton.

Do not implement UI.

### Exit criteria
- repository boots locally;
- schema tests pass;
- architecture docs match this spec.

---

## Phase 1 — Envelope and instrument normalization

Deliver:
- `AgentExecutionEnvelope`;
- JSON Schema / Protobuf;
- validation;
- normalized instrument model;
- fixture library.

### Exit criteria
- invalid envelopes rejected;
- quantity/notional XOR enforced;
- versioning works.

---

## Phase 2 — Identity and attestation

Deliver:
- SPIFFE/SPIRE local setup;
- workload identity verification;
- A0/A1/A2 taxonomy;
- verification metadata.

### Exit criteria
- valid workload accepted;
- forged/expired identity rejected;
- model identity never falsely upgraded.

---

## Phase 3 — Authority

Deliver:
- `AuthorityGrant`;
- grant lifecycle;
- revocation;
- limit evaluation.

### Exit criteria
- full authority test matrix passes.

---

## Phase 4 — Policy runtime

Deliver:
- policy authoring schema;
- compiler;
- signed bundle;
- deterministic evaluator;
- staged deployment metadata.

### Exit criteria
- hard policy runs locally;
- no cloud dependency;
- policy version recorded in decision.

---

## Phase 5 — Broker lifecycle

Deliver:
- `BrokerAdapter`;
- FakeBroker;
- Alpaca Paper;
- order lifecycle;
- reconciliation;
- idempotency.

### Exit criteria
- timeout/retry matrix passes;
- no duplicate order from ambiguous timeout.

---

## Phase 6 — Event backbone and evidence

Deliver:
- NATS JetStream;
- event schemas;
- append-only evidence;
- idempotent consumers.

### Exit criteria
- at-least-once duplication does not corrupt state;
- evidence timeline reconstructs order flow.

---

## Phase 7 — Parent Intent

Deliver:
- deterministic clustering;
- confidence;
- principal aggregation;
- fragmentation detection.

### Exit criteria
- S06 and S07 pass.

---

## Phase 8 — Fleet telemetry

Deliver:
- ClickHouse pipeline;
- rolling windows;
- cohorts;
- dependency observations.

### Exit criteria
- 10k intents/sec sustained benchmark target attempted and measured;
- no hot-path dependency on ClickHouse.

---

## Phase 9 — Fleet Risk Vector

Deliver:
- directional imbalance;
- temporal burst;
- dependency concentration;
- coverage/confidence;
- baseline engine.

### Exit criteria
- metrics are explainable;
- no arbitrary HRI exists.

---

## Phase 10 — Incident engine

Deliver:
- anomaly events;
- incident creation;
- timeline;
- dependency evidence;
- human action audit.

### Exit criteria
- incident can be replayed from evidence.

---

## Phase 11 — Digital Twin

Deliver:
- market engine;
- agent engine;
- execution engine;
- assurance engine;
- deterministic experiments;
- seed control.

### Exit criteria
- repeated same-seed experiment produces same result within tolerance.

---

## Phase 12 — Stress library

Deliver all S01–S12 scenarios.

### Exit criteria
- each scenario has expected assertions;
- S12 proves normal consensus is not automatically blocked.

---

## Phase 13 — Shadow mode controls

Deliver:
- hypothetical THROTTLE;
- REQUIRE_APPROVAL;
- ISOLATE_COHORT;
- READ_ONLY;
- comparison report.

### Exit criteria
- no fleet auto-enforcement unless explicitly enabled;
- would-have-action recorded.

---

## Phase 14 — Console

Deliver six surfaces only:
- Fleet;
- Flow;
- Dependencies;
- Incidents;
- Lab;
- Controls.

### Exit criteria
- no hidden operational dependency on the UI;
- key evidence can be inspected.

---

## Phase 15 — Security/performance/chaos hardening

Deliver:
- invariant tests;
- tenant isolation;
- benchmark harness;
- chaos suite;
- outage tests.

### Exit criteria
- all mandatory invariants pass;
- benchmark results documented.

---

## Phase 16 — Second-adapter proof

Deliver:
- second broker adapter or fully independent contract implementation.

### Exit criteria
- zero core changes required for broker-specific types.

---

# 58. CHECKPOINT RULES FOR CLAUDE/CODEX

At the end of every phase the implementer MUST produce:

```text
PHASE
STATUS
FILES CHANGED
SCHEMA CHANGES
MIGRATIONS
TESTS ADDED
TEST RESULTS
BENCHMARKS
KNOWN LIMITATIONS
SECURITY IMPACT
DEVIATIONS FROM SPEC
NEXT PHASE ENTRY CONDITIONS
```

Any deviation from this document must:
1. stop the phase;
2. describe the conflict;
3. propose an ADR;
4. wait for explicit approval before architectural deviation.

Implementation convenience is NOT sufficient reason to deviate.

---

# 59. PROHIBITED IMPLEMENTATION SHORTCUTS

Claude/Codex MUST NOT:

- call an LLM to decide hard financial policy;
- store broker secrets in `.env` for production design;
- use ticker symbols as canonical instrument IDs;
- silently retry unknown broker submissions;
- bypass Authority Grants;
- rewrite historical evidence;
- build a generic “AI risk score” with arbitrary weights;
- introduce Neo4j without an approved ADR;
- introduce Kafka without an approved ADR;
- split the monorepo without an approved ADR;
- make ClickHouse part of the synchronous hard-policy path;
- treat Redis as source of truth;
- infer model identity from SPIFFE identity;
- claim paper trading simulates market impact;
- let the web console become required for execution;
- implement autonomous fleet blocking before shadow mode exists;
- implement real-money execution in V0;
- implement portfolio recommendations;
- implement stock-selection intelligence;
- turn MCP into the core domain model;
- add decorative AI features that do not satisfy a defined requirement.

---

# 60. CODE QUALITY RULES

Required:
- typed domain objects;
- explicit errors;
- no silent fallback from VERIFIED to DECLARED;
- no magic risk thresholds without documented provenance;
- deterministic unit tests;
- property tests where valuable;
- integration tests using containers;
- test fixtures committed with versioning;
- database migrations reviewed and reversible when technically possible;
- OpenAPI generated from canonical API schema where possible;
- schema compatibility tests.

Go:
- no unnecessary reflection in hot path;
- context propagation;
- structured errors;
- interfaces at boundaries, not everywhere.

Python:
- typed interfaces where practical;
- deterministic seeds;
- vectorized operations;
- no notebook-only production logic.

TypeScript:
- strict mode;
- generated API types;
- no duplicated backend domain truth.

---

# 61. DOCUMENTATION REQUIRED

The repository must maintain:

```text
docs/adr/
docs/architecture/
docs/api/
docs/threat-model/
docs/operations/
docs/runbooks/
```

Minimum runbooks:
- broker timeout / unknown state;
- policy rollback;
- intelligence cloud outage;
- NATS outage;
- Redis outage;
- ClickHouse outage;
- compromised workload identity;
- emergency cohort halt;
- tenant incident investigation.

---

# 62. INITIAL COMMERCIAL WEDGE

The technical architecture supports advanced fleet risk.

The initial commercial value should be positioned around problems that exist today:

```text
agent identity
delegated authority
order provenance
deterministic policy
evidence
shadow mode
incident reconstruction
```

Fleet-level intelligence becomes the future moat:

```text
dependency concentration
behavioral baselines
abnormal consensus
collective-risk detection
Digital Twin
adaptive containment
```

This sequencing MUST guide product prioritization.

---

# 63. PROVISIONAL MOAT

The code itself is not the moat.

The moat, if developed, is expected to arise from the combination of:

```text
AgentExecutionEnvelope ecosystem
+
agent provenance
+
delegated authority
+
economic-intent lineage
+
fleet dependency graph
+
behavioral baselines
+
incident corpus
+
stress-scenario library
+
broker integrations
+
calibrated collective-risk models
```

Do not market an uncalibrated algorithm as proprietary risk science.

---

# 64. ORIGINALITY CONSTRAINT

The team must preserve product-category separation.

Comparison:

```text
Agent-enabled investment platform
AI Agent
  ↓
Investment capability
```

Our platform:

```text
AI-generated financial action
  ↓
Attribution
  ↓
Authority
  ↓
Intent lineage
  ↓
Assurance
  ↓
Fleet intelligence
  ↓
Customer-controlled financial action
```

A platform similar to Zesty, Robinhood, Public, or an agent-enabled broker could theoretically be a customer or integration partner.

Therefore, the project MUST NOT drift into building their consumer-facing product.

---

# 65. FUTURE WORK — EXPLICITLY OUT OF V0

Possible later phases, not authorized now:

- calibrated HRI / probabilistic fleet-risk model;
- provider-attested model identity;
- FIX native integration;
- additional broker/exchange adapters;
- crypto venue support;
- options-specific risk;
- multi-asset exposure graph;
- exchange-level surveillance integration;
- real-time market-impact model;
- agent reputation;
- federated learning across institutions;
- privacy-preserving cross-tenant analytics;
- regulator-facing export format;
- signed hardware attestation;
- confidential computing;
- advanced causal analysis;
- live market deployment;
- real-money execution.

None are V0 requirements.

---

# 66. FINAL MVP SUCCESS DEMONSTRATION

The project is considered demonstrably successful when a reviewer can execute the following scenario:

1. Start the local platform.
2. Launch at least 1,000 synthetic agents.
3. Establish attested identities for controlled workloads.
4. Issue scoped Authority Grants.
5. Submit valid and invalid financial intents.
6. Observe hard policies block invalid authority.
7. Send valid orders to Alpaca Paper.
8. Simulate an ambiguous broker timeout and verify reconcile-first behavior.
9. Fragment a $20,000 economic intent into sub-$5,000 calls and observe Parent Intent detection.
10. Use multiple agents under one principal and observe combined exposure.
11. Trigger one or more fleet-risk scenarios.
12. Observe Fleet Risk Vector and provenance coverage.
13. Observe a dependency concentration.
14. Generate an incident timeline.
15. Run the same Digital Twin experiment twice with the same seed and obtain reproducible results.
16. Trigger S12 Normal Consensus and verify no automatic overreaction.
17. Disable the intelligence cloud/fleet engine.
18. Verify local identity, authority, and hard policy enforcement continue.
19. Inspect append-only evidence mapping:
   `agent → intent → authority → policy → broker order → result`.
20. Confirm no real-money order path exists.

If this sequence cannot be demonstrated, the MVP is not complete.

---

# 67. FINAL IMPLEMENTATION DIRECTIVE

Claude, Codex, or any implementation agent receiving this document must follow these rules:

1. Read this entire document before modifying code.
2. Treat locked ADRs as non-negotiable.
3. Build in phase order unless an explicit dependency requires a documented adjustment.
4. Never silently reinterpret product scope.
5. Never add trading recommendations.
6. Never introduce a new infrastructure dependency merely for sophistication.
7. Prefer deterministic, explainable methods before ML.
8. Expose uncertainty instead of hiding it.
9. Maintain customer-controlled enforcement.
10. Stop and propose an ADR if a locked decision cannot be implemented safely.

The objective is not to build the most feature-rich financial AI product.

The objective is to build:

> **a rigorous, observable, reproducible, customer-controlled assurance layer for AI-generated financial order flow.**

That identity must remain intact through every implementation phase.

---

# END OF MASTER BUILD SPEC
