# ADR-005 — Explicit fail semantics

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Undeclared failure behavior gets decided at 3am by whoever is on call, and it is
almost always fail-open, because fail-open makes the alert stop.

## Decision
Each subsystem must define fail-open, fail-closed, fail-static or reconcile-first
behavior. There is no global fail-open mode.

## Consequences
- The §17 table is normative. ADR-021 completes it for PostgreSQL, NATS, ClickHouse
  and the telemetry buffer.
- Chaos tests (§55) assert one row each.

## Prohibited reinterpretations
- No component may be given failure behavior absent from §17 or ADR-021.
- An operational incident is not authority to add a fail-open path. The ADR is.
