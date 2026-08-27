# ADR-020 — The sizing field is determined by `order_type`

**Status:** ACCEPTED (closes an ambiguity in MASTER_BUILD_SPEC.md §12.3)

## Context

§12.3 requires `quantity XOR notional` — exactly one is the primary sizing field. It
does not say which one each order type requires. A LIMIT order carrying `notional` and
no `quantity` is ambiguous: the share count depends on the fill price, so the platform
would have to derive quantity, and a derived quantity is a trade decision. That is
outside the product boundary (§4, ADR-001).

## Decision

The XOR of §12.3 stands. This ADR adds the per-type restriction:

| `order_type` | `quantity` | `notional` |
|---|---|---|
| MARKET | accepted | accepted |
| LIMIT | **required** | must be null |
| STOP | **required** | must be null |
| STOP_LIMIT | **required** | must be null |

An envelope violating this table is an invalid envelope and is DENIED per §17.

## Consequences

- Phase 1 validation implements this table; it is not left to the broker adapter.
- Callers that only know a dollar amount must either send a MARKET order or convert
  to quantity themselves. The platform will not convert for them.
- Adding a new order type requires adding a row here first.

## Prohibited reinterpretations

- The gateway MUST NOT derive `quantity` from `notional` and a price. Deriving a share
  count from a quoted price is a trading decision, not an assurance decision.
- A missing sizing field MUST NOT default to either column.
