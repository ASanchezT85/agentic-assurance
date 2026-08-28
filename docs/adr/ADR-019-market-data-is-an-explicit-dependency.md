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

## Measured reality (added after the adapter was built)

`adapters/marketdata/alphavantage` exists and is verified against the live provider:
AAPL, 10,198,442,317 of notional on 2026-08-27.

It does not close the gap this ADR describes, and the reason is worth recording
because it will come up again with the next provider.

Alpha Vantage's **free plan serves daily totals only**. `TIME_SERIES_INTRADAY` is a
premium endpoint and the free key is capped at 25 requests per day. So the adapter can
answer "what did this instrument trade over a whole session" and nothing finer, while
the fleet engine measures cohorts over windows of seconds and minutes.

Prorating a daily volume across a one-minute window would assume trading is spread
evenly through the day. It is not: volume is heavily weighted to the open and the
close, so a prorated denominator understates participation at midday and overstates it
at the open. Both produce a number that looks precise and is not, which is the failure
P-004 exists to prevent.

**The adapter therefore refuses any window shorter than a session**, and `P` stays
UNKNOWN for the windows that matter. Closing that needs an intraday feed, which needs
a paid plan or a different provider. Decision item 3 of this ADR is unchanged and now
has a concrete reason behind it.

## Prohibited reinterpretations

- Our own observed order flow MUST NOT be substituted for market volume.
- A missing `P` MUST NOT be rendered or stored as `0`. Zero participation and unknown
  participation are different facts (P-004).
- A daily volume MUST NOT be prorated across a shorter window. Intraday volume is not
  uniform, and a denominator that pretends otherwise is a fabricated one.
