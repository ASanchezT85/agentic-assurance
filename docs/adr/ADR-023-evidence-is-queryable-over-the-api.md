# ADR-023 — The evidence chain is queryable over the API

**Status:** ACCEPTED (extends MASTER_BUILD_SPEC.md §46)

## Context

§66 step 19 requires a reviewer to inspect the append-only chain
`agent -> intent -> authority -> policy -> broker order -> result`. §46's public API
surface exposes no evidence endpoint. §59 forbids the web console from becoming
required for execution, and §17 requires production to be unaffected when the console
is down. Leaving evidence reachable only through the console makes the MVP success
demonstration depend on a component the spec says must be optional.

## Decision

1. Two endpoints are added to the §46 surface, delivered in Phase 6 alongside the
   evidence store:
   - `GET /v1/evidence?correlation_id={id}` — the full ordered chain for a correlation.
   - `GET /v1/intents/{id}/evidence` — the chain rooted at one envelope.
2. Both are read-only, tenant-scoped, and subject to the same authenticated-tenant and
   authorized-actor requirements as every other endpoint in §46.
3. Both return append-only records exactly as stored. Corrections appear as later
   records referencing earlier ones (ADR-009); nothing is collapsed or merged in the
   response.
4. The console consumes these endpoints. It does not have a privileged path.

## Consequences

- §66 step 19 is demonstrable with `curl` and no browser.
- Tenant isolation tests (INV-007) must cover these two endpoints explicitly, since
  they return the richest cross-object data in the system.

## Prohibited reinterpretations

- The response MUST NOT summarize, deduplicate or reorder evidence for readability.
- These endpoints MUST NOT gain write or correction verbs. Corrections are new records
  produced by the systems that own them.
