# ADR-018 — Temporal is deferred out of V0

**Status:** ACCEPTED — **this is a deliberate deviation from MASTER_BUILD_SPEC.md §9.8**

**Approved:** 2026-08-27 by Alexander J Sanchez T, repository owner. Spec §58 requires
explicit approval before an architectural deviation. This is that approval, recorded
here rather than left in a chat log.

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
2. It is removed from `docker-compose.yml` entirely. An earlier revision of this ADR
   kept it as an opt-in Compose profile; the repository owner elected on 2026-08-27 to
   delete it outright, on the grounds that an unused service definition is an
   invitation. There is nothing to accidentally start.
3. Phases 4, 5, 10 and 11 implement their orchestration without it.
4. Reintroducing Temporal as a required dependency requires a future ADR that names
   the measured problem it solves and the phase that owns it.

## Consequences

- §9.8 is superseded on this point. This ADR must be read alongside it.
- Long-running policy rollout and reconciliation are implemented as durable state in
  PostgreSQL plus idempotent workers, consistent with ADR-008 (at-least-once).
- If that state machine grows past what is comfortable to hand-maintain, that is the
  measured justification the reintroduction ADR needs.

## Enforcement

`tests/scope_guard_test.go` asserts mechanically that Temporal stays out: no
`temporal` service and no `temporalio/*` image in `docker-compose.yml`, and no
Makefile, CI workflow or script referencing a temporal profile. The guard is strict
on purpose. Reintroducing Temporal already requires a new ADR, and that ADR is where
this test gets updated. A deferral nobody checks becomes a dependency by accident.

## Prohibited reinterpretations

- Temporal MUST NOT appear on the pre-trade hot path even if reintroduced (§9.8 is
  unambiguous on this and this ADR does not relax it).
- Re-adding the service to `docker-compose.yml` without an approved ADR is prohibited,
  profile or no profile.
- "We already have the container running locally" is not measured justification for
  reintroducing it.
