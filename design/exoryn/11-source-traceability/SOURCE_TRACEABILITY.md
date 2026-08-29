# Source Traceability

This design system is grounded in the supplied repository snapshot.

| Source | Product-design implication |
|---|---|
| `MASTER_BUILD_SPEC.md` §17 | Console unavailable must not affect production; design remains observational. |
| `MASTER_BUILD_SPEC.md` §28 | Coverage, verification, unknown provenance and staleness must be visible; no fake certainty. |
| `MASTER_BUILD_SPEC.md` §48 | Exactly six principal V0 surfaces and their required information. |
| `MASTER_BUILD_SPEC.md` §49 | Incident timeline must explain what/when/who/dependencies/evidence/recommendation/customer action. |
| `MASTER_BUILD_SPEC.md` §59 | Web Console may not become required for execution; no decorative AI features. |
| `docs/adr/ADR-017-phase-0-console-is-a-scaffold.md` | Current minimalist UI was intentional scaffolding/history; Phase 14 opened the six surfaces. |
| `apps/console-web/components/Surface.tsx` | Existing product distinguishes unavailable from empty and uses fixed six-route navigation. Preserve semantics while replacing inline styling. |
| `apps/console-web/app/fleet/page.tsx` | Coverage must remain adjacent to fleet measurements; no composite risk score. |
| `apps/console-web/app/flow/page.tsx` | Evidence chain is the primary reconstruction; UI must not reinterpret recorded outcomes. |
| `apps/console-web/app/dependencies/page.tsx` | Verified/declared/unknown are separate; unknown cannot become a fictitious dependency. |
| `apps/console-web/app/incidents/page.tsx` | Evidence wins over projection; recommendation vs applied control are distinct. |
| `apps/console-web/app/lab/page.tsx` | Seed/source hash/result fingerprint are core to reproducibility; Console does not start runs. |
| `apps/console-web/app/controls/page.tsx` | No mutation buttons; exact scope and gateway-provided `in_force` are authoritative. |
| EXORYN Brand System V1 | Master logo, brand palette, default light direction, typography and voice. |

## Current UI audit summary

The Console already has all six functional routes and live read API helpers, but it has no product-level design system: layout, navigation, tables, unavailable states and forms are mostly inline CSS; the root uses a monospace font globally; no shared semantic tokens, visual hierarchy, operational components or responsive shell exist. V1 therefore treats the current implementation as functional source material rather than a visual system to preserve.
