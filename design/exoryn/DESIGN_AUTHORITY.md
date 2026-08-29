# EXORYN Product Design Authority

**Version:** V1  
**Parent authority:** EXORYN Brand System V1  
**Product authority:** this directory

## Hierarchy

1. `MASTER_BUILD_SPEC.md` and accepted ADRs define product/architecture behavior.
2. EXORYN Brand System V1 defines corporate identity, master logo, brand palette, typography direction and messaging.
3. EXORYN Product Design System V1 defines how those constraints appear in software.
4. Product implementation consumes this system; it does not silently reinterpret it.

## Non-negotiable product rules

- The web Console is observational/read-only.
- Do not add a seventh principal V0 surface.
- Never place a production kill switch, policy activation, grant mutation or order-submission control in the Console unless architecture is explicitly changed through an approved ADR.
- Recommendations must never look like applied controls.
- Unknown, unavailable, stale, declared and verified are different states.
- No design may turn low coverage into high visual certainty.
- Raw evidence remains inspectable.
- Corrections point to the original event; the original is not visually erased.
- Tables remain available where exact inspection is needed, even when summary visualizations are added.
- Sample/mock data in this package is never production data.

## Change control

Material changes to the following require explicit design approval:

- product semantic colors;
- status meanings;
- navigation/surface architecture;
- operational component semantics;
- typography roles;
- information-density rules;
- recommendation/enforcement distinction;
- evidence-timeline behavior.
