# ADR-003 — Customer-controlled enforcement

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
A financial institution cannot outsource final control over market access. If our
cloud can silently loosen a limit the institution set, we have become the risk.

## Decision
Critical market-access and financial-control enforcement must be deployable inside
customer-controlled infrastructure (P-002, P-006).

## Consequences
- assurance-gateway runs in the customer environment.
- Fleet intelligence recommends; customer policy authorizes (INV-009).
- Loss of our cloud degrades insight, never enforcement (§17, ADR-021).

## Prohibited reinterpretations
- No hard limit may live only in our cloud.
- A managed-policy convenience that changes hard limits without versioned, audited
  customer approval is prohibited (§43).
