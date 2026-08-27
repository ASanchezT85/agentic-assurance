# ADR-019 — Market data is an explicit optional dependency; `P` degrades to UNKNOWN

**Status:** ACCEPTED (resolves an omission in MASTER_BUILD_SPEC.md v0.1)

## Context

The Fleet Risk Vector (§22) includes `P`, projected market participation. §24 requires
temporal-burst baselines segmented by volatility, liquidity and event regime. Both
need external market volume and price data. The technology stack (§9) lists no market
data source and no phase delivers one. Phase 8 and Phase 9 would arrive with a metric
they cannot compute, and the tempting fix — estimating participation from our own
observed flow — reports our own order book back to us as if it were the market.

## Decision

1. **Inside the Digital Twin**, market state comes from the simulator's own Market
   Engine (§38). `P` is fully computable there, and every scenario in S01-S12 that
   asserts on `P` runs against the twin.
2. **On the live path**, market data is an optional adapter under
   `adapters/marketdata/`, introduced in Phase 8. It is never on the synchronous
   hard-policy path.
3. **When no market data adapter is configured or the feed is stale**, `P` is emitted
   as `UNKNOWN` with coverage `0`. It is never estimated, interpolated, or derived
   from our own observed flow.
4. The other seven components of the risk vector are computable without market data
   and remain available when `P` is UNKNOWN.

## Consequences

- `adapters/marketdata/` is an additive change to the §8 layout. This is a deviation
  in directory structure and is recorded here rather than made silently.
- The console must render an UNKNOWN `P` as absent, not as zero (§28: the UI must not
  collapse low-confidence data into a precise score).
- Stale-feed handling here is the same mechanism scenario S03 exercises.

## Prohibited reinterpretations

- Our own observed order flow MUST NOT be substituted for market volume.
- A missing `P` MUST NOT be rendered or stored as `0`. Zero participation and unknown
  participation are different facts (P-004).
