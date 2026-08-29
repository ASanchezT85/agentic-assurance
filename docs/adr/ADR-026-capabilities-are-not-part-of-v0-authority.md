# ADR-026: Capabilities are not part of V0 authority

**Status:** Accepted
**Date:** 2026-08-29
**Phase:** V0

## Context

`AuthorityGrant` carried two capability flags, `margin_allowed` and `shorting_allowed`.
They were in the API request, in the Go type, in the grant JSON a customer could read
back, and in two `authority_grants` columns. They were checked by nothing.

The gap was documented. `EnforcedCapabilities()` returned `nil` and a test asserted that
it did, so nobody was misled inside the repository. That is not where the risk was.

A customer issuing a grant with `shorting_allowed: false` reads a control. They size
their exposure against it, they write it into a runbook, and their compliance function
records that agent authority forbids shorting. The platform then authorizes a short,
because deciding whether a SELL is a short needs the account's current position and the
platform does not hold positions until Phase 5. The customer's belief and the system's
behaviour disagree, and the disagreement is invisible from outside — a denial that never
comes looks exactly like an order that never breached the rule.

An unenforced permission is worse than an absent one. An absent one is a gap the
customer can see and compensate for. A present one is a gap they have been told is
covered.

## Decision

Capabilities leave the executable authority contract in V0.

- `authority.Capabilities` and `Grant.Capabilities` are removed, along with
  `EnforcedCapabilities()`. What the grant record contains is now exactly what authority
  evaluation reads.
- `POST /v1/authority-grants` **refuses** a request carrying `margin_allowed` or
  `shorting_allowed`, naming this ADR. Silently ignoring them would leave a client with
  the same false belief and no way to discover it; a 400 tells them today.
- The `authority_grants` columns stay, under a `CHECK` that holds them at false
  (migration 0024). Past grants recorded what a customer asked for, and that is evidence
  even though the platform never applied it. Nothing may write a new one.

The alternative was to enforce them. It was rejected for V0 rather than on principle:
enforcement requires a position model, position data is a broker-sourced fact that the
platform would have to reconcile and hold as truth, and a half-built one would produce
the same defect in a subtler form — capabilities enforced when the position cache is
warm and not when it is cold.

## Consequences

- A client that sends either field gets a 400 with the reason. This is a breaking change
  to an endpoint that has no external users; it is made now because the cost of making it
  later is a customer who has already built on the flag.
- V0 authority is exactly: validity window, status, allowed operations, allowed asset
  classes, instrument allow/deny, and the ceilings in `Limits` — enforced atomically by
  reservation.
- When a position model exists, capabilities come back as a new field with enforcement
  written first and the switch second. A test that turns one on before the check exists
  should fail.
- Margin and shorting are not otherwise prevented. A customer who needs them refused
  today does it with a policy rule, which the policy engine does enforce, or by not
  giving the agent an account that permits them at the broker.
