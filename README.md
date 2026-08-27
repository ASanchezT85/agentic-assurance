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

Phase 0 of seventeen (0 through 16). Phase 0 delivers the monorepo, the locked
architecture decisions, schema locations with a compatibility harness, local
infrastructure, and CI. **It deliberately contains no business logic.** The Go
binaries expose health endpoints. The console renders one static page. The simulator
imports and does nothing.

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
| Go | 1.24 | 1.26.4 |
| Node | 20 | 26.3.0 |
| pnpm | 10 | 11.8.0 |
| Python | 3.11 | 3.14.6 |
| Docker (with compose plugin) | 24 | 29.7.2 |
| make | 4 | optional; see below |

Windows hosts often lack `make`. `scripts/verify.sh` runs the same quality gate.

## Bootstrap

```sh
git clone <repo> && cd agentic-assurance
make bootstrap
```

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

Beyond unit tests, the repository tests itself:

- `tests/structure_test.go` — mandatory directories, root files, all 24 ADRs, all 12
  scenarios, all 15 security invariants documented.
- `tests/scope_guard_test.go` — no broker credentials required, no trading
  recommendation module, no composite risk score, no LLM dependency, and the console
  is still a scaffold (ADR-017).
- `packages/schema_compat_test.go` — the schema versioning policy, enforced.

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
