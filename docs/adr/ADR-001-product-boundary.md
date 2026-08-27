# ADR-001 — Product boundary

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Systems that touch brokerage infrastructure drift toward recommending trades, because
that is the familiar shape. The value of this platform depends on staying on the other
side of that line: it governs order flow it did not generate.

## Decision
The platform MUST NOT generate investment recommendations or strategies. It begins at
the point where a structured financial intent already exists (§4).

## Consequences
- The system answers who acted, with what authority, against which policy. Never what
  to buy, when to enter, or how to maximize returns.
- Brokers and agent-enabled investment platforms are customers or integration
  partners, not products to imitate (§64).
- Marketing may not describe the product as an investment assistant.

## Prohibited reinterpretations
- No ranking, scoring or filtering of instruments by expected return.
- No helpful suggestion of an alternative order when one is denied.
- No feature justified by "the customer asked for a recommendation".
