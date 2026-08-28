# Container view

## The four deployables

ADR-011 fixes four. ADR-016 places the third, which the §8 layout had left without an
entry point.

| Deployable | Language | Entry point | Runs where |
|---|---|---|---|
| assurance-gateway | Go | `cmd/assurance-gateway` | **Customer-controlled infrastructure** (ADR-003) |
| fleet-engine | Go | `cmd/fleet-engine` | Intelligence plane |
| simulation-engine | Python | `python -m simulator.engine` (ADR-016) | Offline / lab |
| console-web | TypeScript / Next.js | `apps/console-web` | Intelligence plane |

## Topology

```text
              agents (REST / SDK / MCP adapter)
                          |
╔═════════════════════════▼═══════════════════════════╗
║  CUSTOMER-CONTROLLED ENFORCEMENT PLANE              ║
║                                                     ║
║  assurance-gateway (Go)                             ║
║    envelope validation, identity, attestation,      ║
║    authority, idempotency, parent-intent state,     ║
║    hard policy, customer controls                   ║
║                                                     ║
║  PostgreSQL  <- source of truth, in-plane           ║
║  Redis       <- ephemeral cache, in-plane           ║
╚════════╤════════════════════════════╤═══════════════╝
         │                            │
   BrokerAdapter                async telemetry
         │                            │
         ▼                            ▼
   Alpaca Paper                 NATS JetStream
   FakeBroker                        │
                            ┌────────┴────────┐
                            ▼                 ▼
                      fleet-engine       evidence flow
                            │
                    ┌───────┴────────┐
                    ▼                ▼
               ClickHouse       PostgreSQL
                    │
                    ▼
             Intelligence API
                    │
            ┌───────┴────────┐
            ▼                ▼
       console-web    simulation-engine
```

## What is deliberately absent

- **Kafka** — NATS JetStream is the V0 event backbone (§9.7).
- **Neo4j** — ADR-010.
- **Temporal** — ADR-018 defers it out of V0. It is not in the Compose file at all.
- **A market data feed on the hot path** — ADR-019 makes it optional and off-path.
- **Any LLM** — ADR-004 and ADR-022.

## Dependency direction

The enforcement plane depends on nothing in the intelligence plane. The reverse is not
true. This is what makes §17 satisfiable: fleet-engine, ClickHouse, NATS and the
console can all be down while the gateway keeps enforcing (ADR-021).

## The Digital Twin (Phase 11)

Four engines, all in `simulator/`, run by the `simulation-engine` deployable
(ADR-016):

| Engine | What it does |
|---|---|
| Market | price walk, spread, depth, square-root impact (spec section 38) |
| Agent | reproducible populations from archetypes (section 39) |
| Execution | fills, rejections, and responses the agent never receives |
| Assurance | a small deterministic gate, so a scenario can compare enforcement on and off |

**Determinism is structural, not careful.** One `SeedSequence` spawns three
independent streams, so adding a draw to the execution engine cannot shift the market
walk. With a single shared generator it would, and an unrelated change would look like
a change in behaviour. Iteration order is fixed everywhere; nothing iterates a set.

There is a test that runs the same seed in two subprocesses and compares. A run that
reproduces inside one process can still depend on something stable there and not
between invocations.

**Two honest limits, stated because they will otherwise be discovered later.**

The impact model is a square-root approximation with conventional coefficients. Spec
section 38 requires it to be documented as an approximation, section 59 forbids
claiming simulated impact is evidence about a real venue, and there is a test
asserting the docstring still says so.

The twin's assurance engine is **not** the production policy engine. It applies
per-order notional and a denied-instrument list, and that is all. The real engine is
Go, and these two can drift: a scenario passing here proves nothing about production
policy. What the twin is for, per ADR-013, is fleet behaviour and market risk. Closing
the gap needs a cross-language boundary nobody has specified.
