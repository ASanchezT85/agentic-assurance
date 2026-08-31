# V0 ELEVENTH AUDIT — THE SURFACE NOBODY RUNS

**Build audited:** `2186216` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** enumerate every exported function and method in `internal/`, ask the coverage
profile which ones ran, and ask the repository which ones anything calls.

The tenth audit ended on its limit: *"I chose which comparisons to look at."* This one
chooses nothing. It takes the whole exported surface — 337 functions and methods — and
sorts it into three piles.

---

## STATUS

```text
337 exported functions and methods in internal/
199 executed by a test that runs without infrastructure
138 never executed, of which 6 were called by nothing at all
```

Three of the six are deleted. Three are kept and named, because each is the exposed half of
something the platform does not do, and removing them would erase the evidence of the gap
rather than close it.

---

## A-11-01 — SIX EXPORTED FUNCTIONS NOBODY CALLS, TWO OF THEM MINE

**Severity: LOW individually. What they say collectively is the finding.**

| Function | Why it is there | Verdict |
|---|---|---|
| `execution.MemoryStore.Retire` | added by the **fourth remediation** so the in-memory store would mirror the tombstone | **deleted** — dead the day it was written |
| `policy.ActivationStore.UsableKeys` | added by the **fourth remediation** for the bootstrap decision | **deleted** — orphaned later in that same pass, when the rule became `Bootstrapped()` and the revoke guard inlined its own query |
| `evidence.Store.MarkPublished` | the single-row marker | **deleted** — superseded by `MarkPublishedBatch` in the third remediation |
| `retention.PostgresStore.MarkVerified` | records that somebody read an archive back and it matched | **kept** — see A-11-02 |
| `incident.Store.StoredCohortID` | the cohort id as stored, for a reader comparing an incident to a measurement | **kept** — the reader it was exposed for does not exist |
| `fleet.NewEWMA` | §24 asks for EWMA among the baseline statistics | **kept** — the statistic is built and nothing computes it |

Two of the six were added by these very audits, four and five passes ago, in the same
remediation that added the guards they were meant to support. That is the part worth sitting
with: a pass that adds a fix and a helper for the fix will orphan the helper when the fix
changes shape three commits later, and nothing in the build says so. The census does, and it
now runs from `tools/surface/census.py` beside the mutation sweep and the refusal census.

---

## A-11-02 — THE ARCHIVE VERIFICATION NOBODY RECORDS

**Severity: MEDIUM. A workflow with a hole where its record should be.**

`retention.Verify` recomputes an evidence chain and says where it stopped matching.
`Exporter.Restore` calls it and refuses to return anything from an archive that fails.
`archive_manifests` has `verified_at` and `verified_by` columns, and
`PostgresStore.MarkVerified` writes them.

Nothing calls `MarkVerified`. So `verified_at` is `NULL` for every manifest that will ever
exist, and the platform cannot answer *"when was this archive last proved to be the one the
manifest describes"* — for a product whose entire proposition is a record somebody can act
on, that is the wrong column to have never written.

It is not one missing call. `Restore` itself has no production caller either: retention
export and restore are a library, with no endpoint, no command and no scheduler. The sixth
audit recorded *"retention has no scheduler"*; this is the sharper version — the verification
half has no scheduler, no caller, and no recorder, and the column that would show it stands
empty.

**Not wired here, deliberately.** Inventing a caller for `MarkVerified` would produce
exactly what this audit just deleted: a function that exists so another function has
somebody to call. What retention needs is an operator entry point, and that is a decision
about the product rather than a fix for a defect.

---

## THE OTHER 132

The rest of the never-executed surface is the PostgreSQL-backed stores — `authority.Store`,
`control.Store`, `evidence` outbox, `policy.ActivationStore`, `execution.PostgresStore`,
`retention.PostgresStore` — plus the fleet engine's projections. All of them are exercised by
the integration, chaos and process suites.

**Which this host cannot run.** Docker Desktop has been stopped for three audits and starting
it needs elevation I do not have, so "covered elsewhere" is what I believe rather than what I
measured, and this report will not dress it as more than that. The number that is measured is
199 of 337 under the suites that run without infrastructure.

---

## THE INSTRUMENT, TWICE WRONG AGAIN

The census reported **138 of 138 uncalled** on its first run — a basic-regex alternation that
matched nothing. A unanimous result means the instrument is broken, not the codebase, and
the second version reported nine orphans of which three were methods called inside their own
declaring file, which the search excluded wholesale.

Both are fixed, both are commented in the tool, and both are the same failure the last four
audits found in their own harnesses. Two limits remain and are stated rather than hidden: a
method reached only through an interface is invisible to a search by name — the three YAML
marshallers are exactly that — and a name shared with another symbol reports as called. The
list is a place to look, not a verdict.

---

## WHAT WAS RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| `go vet` with every build tag (integration, chaos, process, load) | **PASS** — the deletions break no suite, including the ones that cannot run |
| surface census, before | 199 of 337 executed, 6 orphans |
| surface census, after | 199 of 334 executed, 3 orphans, each classified |

**NOT RUN:** integration, race, chaos, process. Fourth audit running.

---

## OPEN RISKS

1. **The full matrix is owed, four audits deep.** It is now the oldest open item in the
   record and the only one that is purely environmental.
2. **Retention has no operator entry point** — no endpoint, no command, no scheduler — for
   export, restore or verification (A-11-02).
3. **`incident.Store.StoredCohortID` and `fleet.NewEWMA`** are exposed halves of readers and
   statistics nobody computes. Kept as visible gaps rather than deleted silently.
4. **The census covers `internal/`.** `cmd/`, the adapters and the console have their own
   surfaces and are not in it.
5. Everything carried from the sixth through tenth audits.

---

## WHAT THIS PASS IS WORTH

The finding I did not expect is that two of the six orphans were mine, added during earlier
audits in this same sequence. The passes that add tests and guards also add helpers, and a
helper outlives the shape of the fix it was written for. Nine audits of measuring the
platform, and this is the first one that measured what the auditing itself left behind.

**V0 remains not self-accepted.**
