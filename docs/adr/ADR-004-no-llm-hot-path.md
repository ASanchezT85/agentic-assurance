# ADR-004 — No LLM in the hot path

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
A non-deterministic component in the authorization path makes every denial
unexplainable and every approval unreproducible, which defeats the audit model.

## Decision
No remote inference, LLM call or non-deterministic model may sit in the critical
synchronous authorization path (P-001).

## Consequences
- Authorization is reproducible: same envelope, same grant, same bundle, same result.
- The latency targets in §50.1 are achievable.
- ADR-022 extends this ban to asynchronous contributions in the assurance decision
  path, closing the gap §29 left open.

## Prohibited reinterpretations
- "It only pre-classifies" is a hot-path contribution.
- A cached LLM verdict is still an LLM verdict.
