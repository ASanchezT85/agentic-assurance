# Fleet Screen Specification

## Purpose
Answer: What is the fleet doing now, how well do we know it, and which cohorts deserve inspection?

## Current source
`GET /v1/fleet/state` from fleet-engine. Current code renders a measurement table.

## V1 hierarchy
1. source/freshness strip;
2. top metrics only where current API supports them;
3. Fleet Risk Vector - separate components, no composite score;
4. coverage stack adjacent to vector;
5. abnormal/cohort region when source supports it;
6. exact measurement table.

## Mandatory
- display observation window;
- display coverage next to each relevant measurement;
- never show unavailable source as 0;
- no generic risk score.

## API-gap rule
The Master Spec asks for connected agents, attestation coverage, intent rate and abnormal cohorts. If the current endpoint does not supply a field, the implementation must not synthesize it. Keep the design slot conditional or add a separately authorized API change.
