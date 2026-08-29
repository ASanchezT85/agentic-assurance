# Visualization Rules

## General
1. Visualization supplements exact data, never replaces it.
2. Axes and units are explicit.
3. Freshness window is visible.
4. Coverage/provenance is adjacent to the metric.
5. No 3D charts, gauges for arbitrary “risk”, or neon decorative telemetry.
6. BUY/SELL direction uses geometry/labels rather than success/danger color semantics.

## Fleet
Use horizontal vector rows, small multiples and time series. Do not collapse the vector into a speedometer.

## Dependencies
Use a node-link graph for exploration plus a sortable table for exact counts. Node size may encode agent concentration; ring/stroke may encode provenance composition. Do not infer causality.

## Incidents
Timeline is the primary visualization. Dependency graph is context. Recommended and enforced control events have separate treatment.

## Coverage
Stacked bars: verified, declared, unknown. Always print numeric percentages.

## Flow
Evidence chain uses chronological nodes. A correction is linked to the corrected event, not overlaid in its place.

## Lab
Use run comparison tables/cards and deterministic fingerprint comparison. Avoid market-chart aesthetics that make EXORYN look like a trading terminal.
