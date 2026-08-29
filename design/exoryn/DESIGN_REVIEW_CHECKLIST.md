# EXORYN Product Design System V1 - Review Checklist

Approve or reject each item before implementation.

## Direction
- [ ] Default light institutional theme is correct.
- [ ] EXORYN logo treatment is correct.
- [ ] Information density is appropriate for financial-institution operators.
- [ ] Product looks like assurance/control infrastructure, not a trading terminal.

## Architecture
- [ ] Six V0 surfaces remain Fleet, Flow, Dependencies, Incidents, Lab, Controls.
- [ ] Console remains read-only.
- [ ] No product design implies the Console is required for execution.

## Semantics
- [ ] Unavailable and empty are visually distinct.
- [ ] Coverage/provenance stays visible.
- [ ] No composite AI risk score is introduced.
- [ ] Recommendation and enforcement are clearly different.
- [ ] Evidence corrections preserve originals.
- [ ] BUY/SELL are not encoded as success/danger.

## Signature product screens
- [ ] Fleet direction approved.
- [ ] Flow/evidence direction approved.
- [ ] Dependencies direction approved.
- [ ] Incidents/War Room direction approved.
- [ ] Lab direction approved.
- [ ] Controls direction approved.

## Platform strategy
- [ ] Mobile should be a monitoring companion, not a full workstation.
- [ ] Desktop should be considered only if it adds workstation/native value.
- [ ] React Native + Expo may proceed to technical evaluation for mobile.
- [ ] Tauri may proceed to technical evaluation for desktop.

## After approval
Create `EXORYN_PRODUCT_UI_IMPLEMENTATION_HANDOFF.md` against the latest post-remediation repository snapshot. Do not implement from sample mockup data; use existing API contracts and explicitly resolve API gaps.
