# ADR-024 — Every security invariant is bound to the phase that introduces it

**Status:** ACCEPTED (completes MASTER_BUILD_SPEC.md §44 and Phase 0 handoff §9)

## Context

§44 declares fifteen mandatory invariants and says they must become automated tests.
The only phase that mentions invariant tests is Phase 15. Taken literally, INV-015
(instrument normalization) would first be tested twelve phases after the code it
guards ships. An invariant tested long after its subject is written is a postmortem,
not a guard.

## Decision

1. Each invariant is owned by the phase that first makes it violable. Its test is
   written in that phase and must pass before the phase exits.
2. Phase 15 becomes the **completeness and regression** gate — it re-runs all fifteen
   under chaos and load — not the first time any of them is checked.
3. Test files live in `tests/security/` and are named for the invariant they guard.

| Invariant | Owning phase | Test |
|---|---|---|
| INV-001 unauthenticated workload cannot create an executable order | 2 | `tests/security/INV-001_unauthenticated_workload_test.go` |
| INV-002 agent cannot exceed its active grant | 3 | `tests/security/INV-002_authority_ceiling_test.go` |
| INV-003 no LLM output bypasses deterministic policy | 4 | `tests/security/INV-003_policy_determinism_test.go` |
| INV-004 no blind duplicate execution after ambiguous timeout | 5 | `tests/security/INV-004_no_blind_retry_test.go` |
| INV-005 intelligence-cloud loss cannot disable local hard limits | 4 | `tests/security/INV-005_local_enforcement_test.go` |
| INV-006 historical evidence cannot be silently mutated | 6 | `tests/security/INV-006_append_only_test.go` |
| INV-007 tenant A cannot observe tenant B | 3 | `tests/security/INV-007_tenant_isolation_test.go` |
| INV-008 unknown provenance is never represented as verified | 1 | `tests/security/INV-008_provenance_no_upgrade_test.go` |
| INV-009 fleet recommends; customer policy authorizes | 13 | `tests/security/INV-009_shadow_authority_test.go` |
| INV-010 no policy reaches production without versioning and validation | 4 | `tests/security/INV-010_policy_lifecycle_test.go` |
| INV-011 Redis loss cannot destroy authoritative control state | 5 | `tests/security/INV-011_redis_not_truth_test.go` |
| INV-012 broker adapter failure cannot corrupt the core domain model | 5 | `tests/security/INV-012_adapter_containment_test.go` |
| INV-013 audit logs and application logs are not interchangeable | 6 | `tests/security/INV-013_log_separation_test.go` |
| INV-014 model identity is never inferred from workload identity | 2 | `tests/security/INV-014_no_identity_inference_test.go` |
| INV-015 invalid instrument normalization cannot reach executable policy | 1 | `tests/security/INV-015_instrument_normalization_test.go` |

INV-007 is assigned to Phase 3 because authority grants are the first tenant-scoped
records persisted; the envelope's `tenant_id` field in Phase 1 is not yet storable and
so not yet leakable.

INV-011 is assigned to Phase 5 because idempotency records are the first authoritative
state that would plausibly be cached in Redis (ADR-015).

## Consequences

- Phase exit criteria in §57 gain the invariant tests listed for that phase.
- Phase 15's scope narrows from "write fifteen tests" to "prove all fifteen hold under
  chaos, load and tenant separation", which is the harder and more useful job.

## Prohibited reinterpretations

- A phase MUST NOT exit with its invariant test written but skipped.
- Reassigning an invariant to a later phase requires amending this ADR, not a comment.
