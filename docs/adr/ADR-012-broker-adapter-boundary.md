# ADR-012 — Alpaca is an adapter, not the product

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
The first broker integration always tries to become the domain model, because its
types are concrete and available while the core types are not yet.

## Decision
The core MUST NOT import Alpaca-specific SDK types. The core depends on the
`BrokerAdapter` abstraction (§18).

## Consequences
- A second adapter or an independent contract implementation must exist before the
  architecture is considered stable (§18, Phase 16).
- Adapter failure cannot corrupt the canonical domain model (INV-012).

## Prohibited reinterpretations
- Re-exporting an Alpaca type from an internal package does not make it ours.
- An Alpaca-shaped field in the envelope is an Alpaca dependency.
