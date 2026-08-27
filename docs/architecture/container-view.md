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
- **Temporal as a requirement** — ADR-018 moves it to an unused optional profile.
- **A market data feed on the hot path** — ADR-019 makes it optional and off-path.
- **Any LLM** — ADR-004 and ADR-022.

## Dependency direction

The enforcement plane depends on nothing in the intelligence plane. The reverse is not
true. This is what makes §17 satisfiable: fleet-engine, ClickHouse, NATS and the
console can all be down while the gateway keeps enforcing (ADR-021).
