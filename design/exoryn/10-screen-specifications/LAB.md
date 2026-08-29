# Lab Screen Specification

## Purpose
Make Digital Twin runs reproducible and comparable, not merely impressive.

## Current source
`GET /v1/simulations`.

## V1 hierarchy
- run list;
- status + scenario + seed;
- scenario source hash;
- result fingerprint;
- selected run details;
- compare runs presentation when enough API data exists.

## Mandatory
- CANCELLED != FAILED;
- hash/fingerprint values remain inspectable;
- Console does not launch runs in V0;
- no “Run simulation” mutation button.
