# Architecture Decision Records

ADR-001 through ADR-014 are **locked** by `MASTER_BUILD_SPEC.md` §6 and are recorded
here unchanged. They are not open for reinterpretation.

ADR-015 and later resolve contradictions, omissions and ambiguities found by auditing
the master spec. Each states which section of the spec it completes or supersedes.

| ADR | Title | Status |
|---|---|---|
| 001 | Product boundary | LOCKED |
| 002 | Canonical intent model | LOCKED |
| 003 | Customer-controlled enforcement | LOCKED |
| 004 | No LLM in hot path | LOCKED |
| 005 | Explicit fail semantics | LOCKED |
| 006 | Workload identity is not model identity | LOCKED |
| 007 | Provenance metadata is mandatory | LOCKED |
| 008 | Event delivery is at-least-once | LOCKED |
| 009 | Evidence is append-only | LOCKED |
| 010 | No graph database in V0 | LOCKED |
| 011 | No premature microservices | LOCKED |
| 012 | Alpaca is an adapter, not the product | LOCKED |
| 013 | Paper trading is not the Digital Twin | LOCKED |
| 014 | No arbitrary HRI score in V0 | LOCKED |
| 015 | Idempotency truth lives in PostgreSQL; Redis is a cache | ACCEPTED |
| 016 | simulation-engine is a Python deployable rooted at `simulator/` | ACCEPTED |
| 017 | The Phase 0 console is a build target, not a UI | ACCEPTED |
| 018 | Temporal is deferred out of V0 | ACCEPTED — deviation from §9.8 |
| 019 | Market data is an explicit optional dependency; `P` degrades to UNKNOWN | ACCEPTED |
| 020 | The sizing field is determined by `order_type` | ACCEPTED |
| 021 | Completed fail-semantics table and named bounds | ACCEPTED |
| 022 | No LLM in the assurance decision path, synchronous or not | ACCEPTED |
| 023 | The evidence chain is queryable over the API | ACCEPTED |
| 024 | Every security invariant is bound to the phase that introduces it | ACCEPTED |

## Deviations from MASTER_BUILD_SPEC.md v0.1

Recorded here so they are never discovered by surprise:

1. **ADR-018 supersedes §9.8 on Temporal.** Temporal moves from required stack to an
   optional, unused Compose profile.
2. **ADR-019 adds `adapters/marketdata/`** to the §8 repository layout. Additive only.
3. **ADR-023 adds two read-only endpoints** to the §46 public API surface.
4. **ADR-021 adds four rows** to the §17 fail-semantics table and names four bounds
   the spec left unspecified.
5. **ADR-015, ADR-020 and ADR-022** tighten §33.3, §12.3 and §29 respectively. They
   remove permitted behavior; they add none.

Nothing here changes ADR-001 through ADR-014.
