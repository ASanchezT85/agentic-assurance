# ADR-007 — Provenance metadata is mandatory

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Fleet risk conclusions are only as good as the provenance behind them. A concentration
number computed over unknown declarations and presented without coverage is a guess
wearing a decimal point.

## Decision
Each dependency assertion must include source, verification level, observation time,
and optionally an evidence reference (P-003).

## Consequences
- Every concentration result carries observed, verified, declared and unknown coverage
  (§25).
- The console must surface confidence and may not collapse it (§28).

## Prohibited reinterpretations
- A missing verification level is UNKNOWN, never a default of DECLARED.
- Coverage may not be omitted because it is embarrassing.
