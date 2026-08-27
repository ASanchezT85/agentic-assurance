# ADR-014 — No arbitrary HRI score in V0

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
A single composite risk number is the most requested and least defensible artifact in
this product category. Weights chosen without calibration are opinion presented as
measurement.

## Decision
V0 exposes a Fleet Risk Vector plus coverage and confidence (§22). A composite HRI
requires empirical calibration and a separate ADR (P-008).

## Consequences
- The console shows eight explainable components, not one number.
- Every component carries its coverage. `P` may be UNKNOWN (ADR-019).
- Uncalibrated algorithms are not marketed as proprietary risk science (§63).

## Prohibited reinterpretations
- A weighted average of the vector with hand-picked weights is an HRI.
- Sorting cohorts by a hidden composite is an HRI with the display turned off.
