# EXORYN Data Language

EXORYN has multiple independent truth axes. Do not compress them into one status badge.

## Required axes

### Availability
`LIVE`, `STALE`, `UNAVAILABLE`, `EMPTY`.

- LIVE: source answered and freshness is within the surface's documented window.
- STALE: source answered but data age exceeds the operational freshness threshold.
- UNAVAILABLE: source could not be read/authenticated/reached. Never render zero metrics.
- EMPTY: source answered successfully and returned no records.

### Attestation
- A0 - unknown origin / observation only.
- A1 - authenticated API identity.
- A2 - workload attested.
- A3 - provider attested; not produced in V0.

A3 must never be visually implied when absent.

### Provenance
- VERIFIED - supported by verified evidence.
- DECLARED - caller/dependency declaration without equivalent verification.
- UNKNOWN - provenance unavailable.

### Enforcement/result
Use the exact recorded result/code wherever possible. Common presentation groups:
- allowed/committed;
- refused/denied;
- unknown/reconciliation required;
- throttled/read-only/isolated;
- expired/revoked/cancelled.

Never translate SELL into danger/red or BUY into success/green.

### Intelligence vs enforcement
- RECOMMENDED - intelligence/shadow recommendation; non-binding.
- ENFORCING - customer-authorized control currently binding.
- REVOKED / EXPIRED - historical control no longer binding.

### Evidence
- RECORDED - append-only event.
- CORRECTION - later event that references an earlier event; original remains visible.
- RECONCILED - outcome obtained by reconciliation, not a new venue submission.

## Visual rule
At most two badges may be shown in a compact table cell. Additional axes move into a detail panel so the interface does not become badge soup.
