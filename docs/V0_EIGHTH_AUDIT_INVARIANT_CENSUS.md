# V0 EIGHTH AUDIT — ONE MUTATION PER INVARIANT

**Build audited:** `7071a4e` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** the seventh audit's sweep, driven by the spec's list instead of my own.

The seventh audit's second open risk said it plainly: *"The sweep covers twelve points, not
every one. A guarantee nobody thought to mutate is exactly the shape of what this pass
found."* Twelve points were the ones I remembered. This pass takes the checklist from the
project's own §1 — fifteen invariants, ten principles — and asks the same question of each:

> §1 claims *"Each one has at least one test that fails when it is violated."*
> Which ones actually do?

Eleven more mutations, one per invariant that the seventh sweep had not touched. **Three
invariants failed the claim.**

---

## STATUS

```text
A-8-01  MEDIUM   INV-010: a bundle that is not ACTIVE could enforce, and nothing failed
A-8-02  MEDIUM   INV-014: a model claim invented from the workload identity, if the
                 caller left it empty, passed every test
A-8-03  LOW      INV-013: evidence printed to stderr instead of recorded, and the guard
                 only knew about loggers
```

All three fixed with behavioural or structural guards, each verified red against the
mutation that found it.

---

## THE MEASUREMENT

| Invariant | Mutation | Caught? |
|---|---|---|
| INV-001 unauthenticated cannot create an executable order | remove `RequireExecutable` | yes — tests/security |
| INV-002 no more authority than the grant | remove `checkLimits` | yes — internal/authority, tests/security |
| INV-003 no LLM output bypasses policy | replace `policy.Evaluate` with ALLOW | yes — internal/gateway, tests/security |
| INV-004 no blind duplicate execution | ignore "already claimed" | yes — internal/execution, tests/security |
| INV-005 intelligence loss cannot disable local limits | treat an unreadable control store as "none apply" | yes — internal/gateway |
| INV-006 evidence is not silently mutated | (privileges, not code — probed in the sixth audit: `INSERT, SELECT` only) | n/a |
| INV-007 tenant comes from the credential | make `RequireTenant` always pass | yes — internal/gateway, tests/security |
| INV-008 unknown provenance is not verified provenance | return A2 from the degraded path | yes — internal/gateway, tests/security |
| INV-009 the customer authorizes enforcement | skip the activation signature check | yes — tests/security (added in the seventh) |
| **INV-010 no policy reaches production unvalidated** | **let a SHADOW bundle enforce** | **NO** |
| INV-011 Redis loss cannot destroy control state | let the cache decide a claim | yes — four packages |
| INV-012 a broker failure cannot corrupt the core model | turn an ambiguous timeout into a rejection | yes — four packages |
| **INV-013 audit logs are not application logs** | **`println` the decision inside `Append`** | **NO** |
| **INV-014 model identity is not inferred from workload identity** | **fill an empty model claim from the SVID** | **NO** |
| INV-015 an invalid instrument cannot proceed | ignore an unmapped instrument | yes — tests/security |

Every invariant is *mentioned* in the tests — INV-010 in four files, INV-013 in two,
INV-014 in three. Mentioning is not covering, and that gap is the whole finding.

---

## A-8-01 — A BUNDLE THAT IS NOT ACTIVE COULD ENFORCE

**INV-010. MEDIUM.**

A policy bundle moves `SIMULATED → SHADOW → CANARY → ACTIVE`, and the gateway refuses to
load anything but the last: `if !bundle.Enforcing()`. Removing that check broke no test.

What it allows is a bundle staged for shadow evaluation — by definition a rulebook nobody
has approved for production — deciding what a customer's agents may do. The lifecycle
exists so the answer to *"who approved this rule"* is never *"whoever last wrote the file"*,
and the check that makes it true was undefended.

`TestOnlyAnActiveBundleEnforces` now runs all three non-production stages through
`FileBundles.Active` and requires a refusal that names the stage.
`TestAnActiveBundleWithoutAnAuthorizationDoesNotEnforce` covers the other half: reaching the
last stage is not the same as the customer having authorized it.

---

## A-8-02 — A MODEL CLAIM INVENTED FROM THE WORKLOAD

**INV-014. MEDIUM.**

`ModelClaims` returns the caller's claims unchanged; SPIFFE proves which workload connected
and says nothing about which model ran inside it. The existing test passes claims that are
already filled in, so this mutation walked past it:

```go
if claims.ModelProvider.Value == "" && !a.SpiffeID.IsZero() {
    claims.ModelProvider = intent.Claim{Value: a.SpiffeID.String(),
                                        Verification: intent.VerificationVerified}
}
```

Four lines, green suite. And they are the *plausible* four lines: filling in what the caller
left empty is exactly what a well-meaning enrichment does, and marking it VERIFIED is what
makes the evidence chain assert something nobody proved.

`TestAnEmptyModelClaimIsNotFilledInFromTheWorkload` covers the empty case: nothing is
invented, and nothing comes back verified.

---

## A-8-03 — EVIDENCE PRINTED INSTEAD OF RECORDED

**INV-013. LOW.**

The guard parses the imports of `internal/evidence` and fails on `log` or `log/slog`. It is
the obvious half — and partly the compiler's anyway, since an unused import does not build.
A `println("audit:", e.EventID)` inside `Store.Append` needs no import at all, and it is the
failure §51 describes: a decision written to a stream that is sampled, rotated, dropped
under pressure and unqueryable, being called an audit trail.

`TestEvidencePackageDoesNotPrint` now refuses `println`, `print`, `fmt.Print`, `os.Stdout`
and `os.Stderr` anywhere in the package, and fails if it finds fewer than three files to
check — a guard that passes by looking in the wrong place is the failure mode it exists to
prevent.

---

## WHAT WAS RUN, AND WHAT WAS NOT

```text
quality gate (scripts/verify.sh)                       PASS
the three new guards, each against its own mutation    RED as required, then green
mutation sweep, invariants (container)                 22 rows measured before the daemon died
```

**NOT RUN, and why.** Docker Desktop stopped part-way through the confirmation sweep and did
not come back on this host: the service is stopped and starting it needs elevation I do not
have. That took with it the integration, race, chaos and process suites, which need
PostgreSQL, ClickHouse, NATS and SPIRE, and the container the sweep runs in.

So this report's re-run is partial. The three fixes are verified individually — each
mutation applied by hand, each new guard observed failing, each reverted — and the full
matrix is owed on a host where the daemon is up. Saying "PASS" for a suite I could not
execute is the one thing these eight audits have consistently found to be worse than the
defect.

---

## THE HARNESS, AGAIN

Two more faults in the sweep itself, both of the kind it exists to find:

1. **It left a mutation applied.** A run ended between applying and reverting — twice now —
   leaving an enforcement point removed in the working tree. `main()` reverts in a `finally`
   now.
2. **It reported a dead daemon as twenty-three compilation failures.** When Docker stopped,
   every row came back `DID NOT COMPILE`, including the baseline. There is a
   `CONTAINER UNAVAILABLE` outcome now, and the sweep stops rather than continuing once the
   baseline is not green: a mutation is only interesting against a suite that was green
   without it.

Three audits in a row have now found the measuring instrument lying about what it measured.
That is worth saying out loud once more, because the platform's whole product is a record of
what happened, and a harness that misreports is the same defect wearing a lab coat.

---

## OPEN RISKS

1. **The full matrix is owed.** See above; the daemon is down on this host.
2. **INV-006 is enforced by privileges, not by code**, so the sweep cannot mutate it. It was
   probed directly in the sixth audit (`evidence_events` is `INSERT, SELECT` to the
   application role) and that probe is not automated.
3. **The census covers invariants, not principles.** P-001 to P-010 are broader statements
   and several have no single enforcement point to remove; P-002 was covered in the seventh
   only because a mutation happened to touch it.
4. Everything carried from the sixth and seventh audits: connection budget undocumented,
   retention per credential-registry tenant and in-process only, `authority_usage` unbounded,
   no endpoint lists registered keys, console unreviewed by a human, one-host capacity.

---

## WHAT THIS PASS IS WORTH

The seventh audit measured twelve guarantees I chose. This one measured the fifteen the
project publishes, and three of them did not have the test §1 claims they have. The
difference between the two passes is entirely the source of the checklist.

The remaining gap is the same one, one level up: the invariants are a list somebody wrote,
and a guarantee the platform relies on but never wrote down is still invisible to this
method.

**V0 remains not self-accepted.**
