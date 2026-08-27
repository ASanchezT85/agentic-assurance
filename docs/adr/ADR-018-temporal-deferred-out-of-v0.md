# ADR-018 — Temporal is deferred out of V0

**Status:** ACCEPTED — **this is a deliberate deviation from MASTER_BUILD_SPEC.md §9.8**

## Context

§9.8 lists Temporal in the required technology stack and assigns it approvals,
reconciliation workflows, policy rollout orchestration, long-running simulations and
incident workflows. However:

- No build phase from 0 through 16 delivers Temporal or anything running on it.
- §56 Definition of Done — MVP does not reference it in any of its 30 conditions.
- Every workload §9.8 assigns to it is delivered by another phase without it:
  reconciliation in Phase 5, policy rollout in Phase 4, long simulations in Phase 11,
  incident workflows in Phase 10.
- The Phase 0 handoff already downgrades it to an optional Compose profile that must
  not be required for boot.

A required dependency that no phase builds and no acceptance criterion checks is not
a requirement. It is an unowned service that will be stood up, half-wired, and then
relied upon by accident.

## Decision

1. Temporal is removed from the V0 required stack.
2. It remains available as an optional `docker-compose` profile (`--profile temporal`)
   that no service depends on and no test requires.
3. Phases 4, 5, 10 and 11 implement their orchestration without it.
4. Reintroducing Temporal as a required dependency requires a future ADR that names
   the measured problem it solves and the phase that owns it.

## Consequences

- §9.8 is superseded on this point. This ADR must be read alongside it.
- Long-running policy rollout and reconciliation are implemented as durable state in
  PostgreSQL plus idempotent workers, consistent with ADR-008 (at-least-once).
- If that state machine grows past what is comfortable to hand-maintain, that is the
  measured justification the reintroduction ADR needs.

## Prohibited reinterpretations

- Temporal MUST NOT appear on the pre-trade hot path even if reintroduced (§9.8 is
  unambiguous on this and this ADR does not relax it).
- The optional profile MUST NOT become a hidden dependency of any test or make target.
