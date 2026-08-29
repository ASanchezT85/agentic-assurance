# EXORYN Product Design System V1

**Status:** DESIGN AUTHORITY DRAFT - ready for product review  
**Scope:** EXORYN Console Web + future mobile/desktop product surfaces  
**Source snapshot:** `agenticassurancecd97393.zip`  
**Brand parent:** EXORYN Brand System V1

This package turns the existing functional V0 console into a coherent product design language without changing the V0 architecture.

## What this V1 establishes

- product design principles;
- product-level design tokens derived from the approved EXORYN brand palette;
- semantic language for attestation, provenance, evidence, controls, severity and availability;
- core UI component contracts;
- operational components specific to EXORYN;
- visualization rules that preserve uncertainty and provenance;
- screen specifications for the six V0 console surfaces;
- web, mobile and desktop patterns;
- high-fidelity static mockups using **sample data only**;
- an interactive local HTML reference;
- source traceability back to the repository and Master Build Spec.

## Critical architectural constraints preserved

1. The Console remains **read-only**.
2. The Console must never become required for execution.
3. V0 keeps exactly six principal surfaces: Fleet, Flow, Dependencies, Incidents, Lab, Controls.
4. No composite generic AI risk score is introduced.
5. Coverage, confidence, provenance and staleness remain visible.
6. Recommendations and actual enforcement are never visually conflated.
7. `Unavailable` is distinct from an empty result. The design must never render an unreachable source as zero.
8. Append-only evidence and corrections remain visible as history, not overwritten state.

## Deliverables

- `EXORYN_PRODUCT_DESIGN_SYSTEM_V1.pdf` - visual design-system specification.
- `prototype/index.html` - interactive local product reference.
- `mockups/` - Fleet, Flow, Dependencies, Incidents, Lab, Controls, Mobile and Desktop reference PNGs.
- `02-tokens/` - JSON, CSS and TypeScript tokens.
- `10-screen-specifications/` - implementation contracts for each existing Console surface.

## Important

This package is a **design specification**, not authorization to modify the remediation branch. Implementation into `apps/console-web` should happen in a separate UI integration pass after explicit approval.

All values shown in mockups are illustrative sample data and must not be interpreted as measured platform results.
