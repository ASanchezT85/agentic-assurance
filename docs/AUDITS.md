# The audit record

Fourteen audit passes were run against EXORYN V0. Each report is kept in full, including
the ones that found little and the ones where the measuring instrument turned out to be
wrong. Nothing here has been edited after the fact to look better.

The order matters more than any single finding: each pass had to use a **different
method**, because a method repeated only finds again what it already knows how to see.

Current high-level state: [`ESTADO_V0.md`](ESTADO_V0.md) (Spanish).

---

## The passes

| # | Report | Method | What it found |
|---|---|---|---|
| 1 | [`V0_AUDIT_REMEDIATION_REPORT.md`](V0_AUDIT_REMEDIATION_REPORT.md) | remediation of an external review | first round of fixes against the spec |
| 2 | [`V0_SECOND_AUDIT_REMEDIATION_REPORT.md`](V0_SECOND_AUDIT_REMEDIATION_REPORT.md) | remediation of an external review | second round |
| 3 | [`V0_THIRD_AUDIT_REMEDIATION_REPORT.md`](V0_THIRD_AUDIT_REMEDIATION_REPORT.md) | remediation of an external review | third round |
| 4 | [`V0_FOURTH_AUDIT_REMEDIATION_REPORT.md`](V0_FOURTH_AUDIT_REMEDIATION_REPORT.md) | remediation plus the key-registration addendum | idempotency permanence, policy CAS history, outbox acknowledgement ordering |
| 5 | [`V0_FIFTH_AUDIT_SELF_REVIEW.md`](V0_FIFTH_AUDIT_SELF_REVIEW.md) | self-review of the fourth remediation | **CRITICAL**: administrative evidence never reached the bus · the outbox had no retention (4.3 GB of an 8.3 GB database) |
| 6 | [`V0_SIXTH_AUDIT_RECONCILIATION.md`](V0_SIXTH_AUDIT_RECONCILIATION.md) | read the evidence the running system produced, not the code | **HIGH**: the release path had never worked, and the platform had been saying so in its own record |
| 7 | [`V0_SEVENTH_AUDIT_MUTATION_SWEEP.md`](V0_SEVENTH_AUDIT_MUTATION_SWEEP.md) | mutate chosen points and see which tests notice | what the suite does and does not actually pin |
| 8 | [`V0_EIGHTH_AUDIT_INVARIANT_CENSUS.md`](V0_EIGHTH_AUDIT_INVARIANT_CENSUS.md) | one mutation per invariant, from the spec's own list | **MEDIUM ×3**: a non-ACTIVE bundle could enforce (INV-010); a model claim inferred from workload identity (INV-014); evidence printed instead of recorded (INV-013) |
| 9 | [`V0_NINTH_AUDIT_REFUSAL_CENSUS.md`](V0_NINTH_AUDIT_REFUSAL_CENSUS.md) | census every refusal code by execution coverage | 28 refusal codes no test had ever produced |
| 10 | [`V0_TENTH_AUDIT_BOUNDARIES.md`](V0_TENTH_AUDIT_BOUNDARIES.md) | every comparison that decides money or time, tested at the exact boundary | **MEDIUM**: three implementations of one rule, two of them right |
| 11 | [`V0_ELEVENTH_AUDIT_SURFACE.md`](V0_ELEVENTH_AUDIT_SURFACE.md) | census all 337 exported symbols: executed, called, orphaned | six exported functions nobody called — **two of them added by earlier audits in this same sequence** · archive verification that nothing records |
| 12 | [`V0_TWELFTH_AUDIT_EDGES.md`](V0_TWELFTH_AUDIT_EDGES.md) | the same census over `adapters/` and `cmd/`, then read the unexercised methods | **HIGH**: the Alpaca adapter discarded parse errors on the execution path — an order that filled could be recorded as having filled nothing |
| 13 | [`V0_THIRTEENTH_AUDIT_CONSOLE.md`](V0_THIRTEENTH_AUDIT_CONSOLE.md) | compare the Console's declared types against what the backend actually serialises | **MEDIUM**: 64-bit counts arrived as quoted strings, so the Dependencies surface ranked `"9"` above `"10"` |
| 14 | [`V0_FOURTEENTH_AUDIT_SECRETS.md`](V0_FOURTEENTH_AUDIT_SECRETS.md) | take the code's own absolute claims — *never*, *cannot*, *must not* — as assertions to test | **HIGH**: the analytical store's password rode in a URL query string, and Go's `*url.Error` printed it into three log sites |

Severity totals across the ten self-run passes (5–14): **1 critical, 4 high, 6 medium,
1 low**, plus the censuses.

---

## What the record is worth

**These passes found real defects. They are not a proof of correctness**, and none of them
substitutes for independent review by someone else. Two things in particular are worth
saying out loud:

**The instrument lied in almost every pass.** The surface census first reported "138 of
138 uncalled" — a regex alternation that matched nothing. The mutation harness reported a
dead Docker daemon as 23 compilation failures. The tenth audit's first probe was simply
wrong about what policy does. Every report says so about itself, because a unanimous
result means the instrument is broken, not the codebase.

**Methods that start from a document find only what the document mentions.** The passes
that started from the running system, or from the code itself, found what the documents
did not know. The sixth audit read the evidence chain and discovered a release path that
had never worked; no amount of re-reading the specification would have surfaced it.

And the finding that was least expected: in the eleventh pass, two of the six orphaned
functions had been **written by earlier audits in this same sequence** — helpers added
alongside a fix, orphaned three commits later when the fix changed shape. A process that
audits a system also leaves residue in it.

---

## The instruments

The tooling written for these passes is kept in [`../tools/`](../tools/):

```text
tools/mutation/sweep.py      apply a mutation, run the suite, record what noticed
tools/refusals/census.py     which refusal codes has any test ever produced
tools/surface/census.py      which exported symbols are executed, called, or neither
```

Each one carries, in its own header, the bug it had and how it was caught.
