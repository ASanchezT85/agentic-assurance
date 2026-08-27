# ADR-006 — Workload identity is not model identity

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
SPIFFE/SPIRE proves which workload connected. It says nothing about which model
produced the reasoning inside that workload. Conflating them sells attestation we do
not have.

## Decision
SPIFFE/SPIRE attests workloads. It MUST NOT be presented as proof of model reasoning.

## Consequences
- Attestation levels A0 through A2 describe the workload (§11). A3, provider-attested
  model identity, is never simulated and never claimed.
- `runtime_claims` in the envelope stay DECLARED unless a provider attests them.
- INV-014 tests this from Phase 2 (ADR-024).

## Prohibited reinterpretations
- An attested workload that declares a model does not thereby have a verified model.
- Concentration over declared models must report declared coverage, not verified
  coverage (§25).
