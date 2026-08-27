# ADR-009 — Evidence is append-only

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Audit evidence that can be edited is not evidence. A correction path that rewrites
history destroys the ability to reconstruct what the system believed at decision time.

## Decision
Corrections reference prior evidence rather than rewriting it (P-007).

## Consequences
- The evidence store has no UPDATE and no DELETE on the hot path.
- Incident timelines (§49) are reconstructable because nothing was overwritten.
- ADR-023 exposes the chain over the API without collapsing corrections.

## Prohibited reinterpretations
- A cleanup migration that squashes superseded records is prohibited.
- Retention expiry removes a whole bounded window. It is never selective editing.
