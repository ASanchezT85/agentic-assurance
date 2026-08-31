# EXORYN Portfolio V0 — freeze note

**Date:** 2026-08-31
**Branch:** `portfolio/exoryn-v0-closeout`
**Core commit at freeze:** `6199342` — unchanged by this closeout

This is **not** a production release note. It records what the repository contains at the
point it was prepared for public portfolio publication, and what it deliberately does not
contain.

---

## What this freeze contains

The closeout changed presentation, documentation and the Console's visual layer. It did
not change financial behaviour, migrations, or any enforcement path.

```text
docs/banner.svg               repository-native hero banner, canonical brand geometry
README.md                     rewritten as a portfolio case study
docs/AUDITS.md                index of the fourteen audit passes
docs/DEMO.md                  a runnable walkthrough script
docs/PORTFOLIO_RELEASE.md     this note
docs/screenshots/             six real runtime screenshots of the Console
apps/console-web/             the canonical shell: left navigation, 64 px top bar,
                              loading state, responsive breakpoints
```

## What is demonstrated

A deterministic control plane for AI-generated financial order flow:

- **Attribution** — workload identity plus signed execution envelopes; the tenant comes
  from the credential and never from the request, enforced again by PostgreSQL RLS.
- **Authority** — tenant-scoped grants with exact fixed-point money and atomic
  reservations; two replicas cannot overspend one grant.
- **Policy** — signed, versioned bundles activated by a customer-signed authorization,
  with a compare-and-swap history that cannot branch.
- **Idempotency** — a permanent identity that survives its own retention sweep, so a
  pruned request cannot reach a venue twice.
- **Evidence** — an append-only chain written in the same transaction as the decision,
  published through a transactional outbox.
- **Fleet intelligence** — cohorts, a Fleet Risk Vector carrying its own coverage, and
  incident reconstruction that recommends and never enforces.
- **Simulation** — a deterministic Digital Twin with a scenario source hash and a result
  fingerprint.
- **Failure handling** — enforcement survives the loss of ClickHouse, NATS, Redis and the
  intelligence plane, and fails closed on the loss of PostgreSQL.

## Validation snapshot

```text
2026-08-31 · re-run on the closeout tree, after every change in it

Integration        124 pass ·  3 skip · 0 fail
Race               clean, no data races
Race + integration clean
Chaos                9 / 9
Process              2 pass · 1 skip
Quality gate       green, Console lint / typecheck / production build included
```

Integration gained two tests in this closeout: the Console's unavailable-is-not-zero
promise and its numeric-count comparison, both driving the real production Console as a
process, both verified red first.

Skips are reported as skips. The skipped process test needs Alpaca Paper credentials, and
with them supplied it still cannot start on this Windows host because Smart App Control
blocks the freshly built binary — a host policy, not a code failure. The same tree
verifies green in the project's Linux container.

## The paper-only boundary

```text
no real-money execution path
both broker adapters refuse a non-paper endpoint
no venue cancellation of an order already working
no investment recommendation of any kind
no LLM in the decision path — enforced by a test
```

No real credential is required to build, run or test this repository. No real credential
has ever been committed to it; the current tree and all 137 commits of history were
scanned before publication.

## Known limitations

Carried from `docs/ESTADO_V0.md` and re-checked at freeze:

- retention export, verify and restore have no operator entry point, so
  `archive_manifests.verified_at` is never written;
- `broker.Adapter` requires seven methods and the platform calls two;
- `authority_usage` grows unbounded, and no endpoint lists registered keys;
- Tradier's read side and the object store are not exercised by an executing test;
- the connection budget is undocumented — 92 of 100 connections were reached under load;
- one process test cannot start on the current Windows host;
- there is no independent security certification and no third-party penetration test.

The Console's behavioural gap named in `ESTADO_V0.md` — nothing asserting that an
unavailable source renders as unavailable rather than as zero — is closed by this
closeout.

## What is intentionally not planned in this repository

```text
real-money execution
new financial features
new broker capabilities
a seventh Console surface
Console write paths of any kind
mobile or desktop applications
a commercial backend or roadmap
```

The repository is a **frozen portfolio V0**, subject only to critical security fixes,
dependency maintenance and broken-build fixes, if it is maintained at all.

## Suggested tag

```text
v0.1.0-portfolio
```

Not `v1.0`, not `stable`, not `GA`.
