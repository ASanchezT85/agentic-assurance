# ADR-013 — Paper trading is not the Digital Twin

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Broker paper trading is easy to mistake for a simulator. It exercises order lifecycle
mechanics. It does not model fleet behavior and it does not model market impact.

## Decision
Alpaca Paper tests broker lifecycle. Our simulator tests fleet behavior and
market-risk scenarios. They are different systems with different purposes.

## Consequences
- The Digital Twin runs independently of Alpaca (§56 item 22).
- The twelve stress scenarios run against the simulator, not against paper trading.

## Prohibited reinterpretations
- Paper trading results MUST NOT be presented as market-impact evidence (§59).
- A scenario is not reproducible merely because paper trading accepted the orders.
