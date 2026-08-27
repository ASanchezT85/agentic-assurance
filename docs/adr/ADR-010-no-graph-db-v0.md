# ADR-010 — No graph database in V0

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Dependency relationships look like a graph, and that resemblance pulls hard toward
adding a graph database before anyone has measured the query load.

## Decision
Dependency graph storage uses PostgreSQL plus analytical projections in ClickHouse.
Neo4j or similar requires a future ADR with measured justification.

## Consequences
- One less stateful system to operate, secure and back up in V0.
- Dependency queries (§48.3) are relational plus columnar until proven insufficient.

## Prohibited reinterpretations
- "The query is more natural in Cypher" is not measured justification.
- An embedded graph library used as a store is the same decision under another name.
