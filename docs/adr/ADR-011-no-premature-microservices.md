# ADR-011 — No premature microservices

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Splitting services before ownership or scaling pressure exists multiplies deployment,
tracing and failure modes while the domain boundaries are still moving.

## Decision
V0 uses four primary deployables: assurance-gateway, fleet-engine, simulation-engine,
console-web. Service extraction requires measured scaling or ownership justification.

## Consequences
- The ten bounded contexts of §10 are packages, not services.
- ADR-016 places simulation-engine, which the §8 layout left without an entry point.
- The monorepo is not split during V0 (§8).

## Prohibited reinterpretations
- A bounded context is not automatically a service.
- Reducing the count to three by folding the simulator into fleet-engine is prohibited
  (ADR-016).
