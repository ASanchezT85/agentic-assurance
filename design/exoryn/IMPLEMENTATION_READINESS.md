# Implementation Readiness / Known API Gaps

The design system intentionally shows the target information hierarchy, but implementation must use only data actually provided by the current backend.

## Fleet
Current `/v1/fleet/state` provides cohort/window measurements and coverage. The Master Spec also describes connected agents, attestation coverage, intent rate and abnormal cohorts at the surface level. Do not synthesize global KPIs if the current read model cannot supply them.

## Flow
Current APIs support recent intents and evidence reconstruction. Ready for visual implementation without changing write behavior.

## Dependencies
Current `DependencyRow` provides dependency-level concentration/provenance counts but not a general graph edge contract between dependency nodes. A true relationship graph requires backend relationship data. Until then, render concentration nodes/table without fabricated edges.

## Incidents
Current incident projection plus evidence chain supports the core V1 incident experience. Context panels must be conditional on fields/evidence actually present.

## Lab
Current simulation list supports scenario, seed, source hash, status and result fingerprint. A rich compare-runs view may require additional detail endpoints/read models.

## Controls
Current `/v1/controls` supports authorized control records. Master Spec also mentions active policy bundle, grant state and audit history; those should not appear as real panels until read APIs exist.

## Mobile / Desktop
No native apps exist in the supplied snapshot. V1 defines product roles and visual patterns only.
