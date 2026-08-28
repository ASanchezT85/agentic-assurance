# ADR-012 — Alpaca is an adapter, not the product

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
The first broker integration always tries to become the domain model, because its
types are concrete and available while the core types are not yet.

## Decision
The core MUST NOT import Alpaca-specific SDK types. The core depends on the
`BrokerAdapter` abstraction (§18).

## Consequences
- A second adapter or an independent contract implementation must exist before the
  architecture is considered stable (§18, Phase 16).
- Adapter failure cannot corrupt the canonical domain model (INV-012).

## Verified in Phase 16

Both halves of section 18's requirement were built, because they prove different
things.

`tests/contract` defines the contract behaviourally and runs it against all three
adapters. An interface says what an adapter must have; it cannot say that a timeout
means UNKNOWN rather than failed, or that a lookup by our identifier must find an
order the venue created under it. Those rules lived in comments and in three separate
test files that could each have been right about a different contract.

`adapters/tradier` is the second venue, chosen for shapes that press on the
abstraction rather than confirm it: form-encoded requests, bearer auth, a partly
overlapping status vocabulary with an `error` state Alpaca has no equivalent for,
account-scoped paths, single-object-or-array responses, no notional sizing, and no
lookup by client order id.

**Adding it required zero changes to `internal/`.** Verified by `git status`: the only
new paths were `adapters/tradier/` and `tests/contract/`.

The second adapter also found something, which is the real argument for building one.
Tradier's order tag accepts letters, digits and dashes; the platform generates client
order ids containing underscores. An adapter that quietly rewrote the id would leave
an order at the venue under a name nothing could ever look up, and INV-004 would be
unenforceable against that venue while every test still passed. The adapter refuses
instead and names the reason, because the fix belongs in how identifiers are generated
rather than in the adapter.

## Prohibited reinterpretations
- Re-exporting an Alpaca type from an internal package does not make it ours.
- An Alpaca-shaped field in the envelope is an Alpaca dependency.
