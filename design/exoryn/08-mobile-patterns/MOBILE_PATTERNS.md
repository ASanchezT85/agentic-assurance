# Mobile Product Pattern - iOS / Android

## Product role
The native mobile application should be an **operational monitoring companion**, not a compressed workstation.

Primary tasks:
- fleet/system overview;
- incident awareness;
- incident timeline inspection;
- recent flow/evidence inspection;
- search/copy identifiers;
- secure tenant switching;
- native notifications when product requirements authorize them.

Do not put Lab graph exploration or complex dependency analysis into the first mobile release.

## Navigation proposal
Bottom tabs: Overview, Incidents, Fleet, Flow, More.

## Mobile home
Source health, active incidents, fleet coverage, intent rate, high-level vector components and freshness.

## Platform recommendation for implementation study
React Native + Expo is the leading candidate because the product already uses TypeScript/React. Share tokens, schemas, API client and domain presentation logic; do not force DOM components into React Native.

This is a design/architecture recommendation, not an implementation lock.
