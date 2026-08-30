# V0 SEVENTH AUDIT — WHAT THE TESTS DO NOT CATCH

**Build audited:** `ba7acfd` · **Build after remediation:** `afa4f45` · **Date:** 2026-08-30
**Method:** mutation sweep. The subject is the test suite, not the code.

The sixth audit read the platform's own evidence and found a guarantee that had never
worked. This one asks the next question: **which guarantees would survive being deleted?**

Twelve mutations, each removing exactly one thing the platform claims it enforces, applied
one at a time to a clean tree, each followed by the suites the quality gate runs — unit,
security, scenarios, contract. A mutation nothing notices is a guarantee with no guard.

The sweep runs inside the Linux container the race detector already uses. This
workstation's Smart App Control evaluates each freshly written executable and its verdict
is per file and sticky, so a mutated package produces a binary that is blocked and stays
blocked however often it is retried — the first attempt returned twelve inconclusive rows
before that was understood.

---

## THE MEASUREMENT

Baseline: green.

| Enforcement point removed | Caught by the gate's suites? |
|---|---|
| INV-002 authority ceiling (`checkLimits`) | **yes** — internal/authority, tests/security |
| INV-004 duplicate submission (the `!claimed` branch) | **yes** — internal/execution, tests/security |
| INV-007 tenant from the credential (`RequireTenant`) | **yes** — internal/gateway, tests/security |
| envelope signature verification | **yes** — internal/gateway |
| exact money on the wire (excess precision) | **yes** — internal/money, tests/security |
| agent-key registrar privilege | **yes** — tests/security |
| activation-key registrar privilege | **yes** — tests/security |
| **INV-009 policy activation signature** | **NO** |
| **F4-B002 policy predecessor check** | **NO** |
| **F4-B001 idempotency tombstone check** | **NO** |
| **A-5-01 outbox enqueue on a recorded event** | **NO** |
| **P-002 grant issuer privilege** | **NO** |

Seven of twelve. The five that survived are §§A-7-01 to A-7-03.

---

## A-7-02 — THE ISSUER PRIVILEGE HAD NO TEST AT ALL

**Severity: HIGH. P-002, and the one the platform names first.**

`IssueGrantHandler` checks `MayIssueAuthority` before creating a grant. §3 of the project
status lists that separation first among the three it calls deliberate: *"A credential that
could do both would let an agent raise its own ceiling, and INV-002 would be enforced
against a limit the party under it can move."*

Deleting the check broke nothing. Searching afterwards confirmed why:

```text
$ grep -rn "MayIssueAuthority" tests/
(nothing)
```

Not a weak test — no test. The route-documentation guard knows the endpoint exists and the
tenant-transport guard knows it authenticates; nothing asked whether an agent credential
could widen its own authority. This is the platform's central promise, and it was one line
away from being untrue with a green suite.

`TestOnlyAnIssuerMayCreateAGrant` now covers it: the issuer may, the submission credential
gets 403 and nothing is stored, an unauthenticated caller gets 401.

---

## A-7-03 — REVOCATION HAD NO PRIVILEGE CHECK

**Severity: HIGH. Availability, found while writing the test for A-7-02.**

`RevokeGrantHandler` authenticates the caller, establishes the tenant — and then revokes.
There was no privilege check to mutate, and nothing anywhere recorded that as a decision.

Any credential in the tenant could cut any grant in it. A compromised agent credential —
one issued only to trade — could stop every other agent in the customer's fleet, one
request at a time. The platform's own documentation calls this endpoint "the emergency
lever"; an emergency lever any agent can pull against its peers is a denial of service with
extra steps.

Fixed: revocation requires `MayIssueAuthority`. It still needs no signature, no second
party and no healthy submission path — the lever stays fast, which is the property that
matters during an incident. What it no longer is, is unprivileged.

**This is a behaviour change and a deployment must know it.** A credential that could revoke
yesterday and is not in `GATEWAY_GRANT_ISSUERS` cannot revoke today. If the intended model
really is "anyone in the tenant may pull the lever", that is a decision to write down in an
ADR, not to leave as the absence of a check.

---

## A-7-01 — FOUR GUARANTEES DEFENDED ONLY OUTSIDE THE GATE

**Severity: MEDIUM. Coverage topology.**

Four mutations survived because the tests that would catch them need PostgreSQL, ClickHouse,
NATS and SPIRE, and the gate deliberately does not run those:

```text
the policy activation signature check      proved in tests/integration/policy_activation_test.go
the predecessor check on a transition      proved in tests/integration/policy_transition_cas_test.go
the tombstone check on a claim             proved in tests/integration/idempotency_permanence_test.go
the outbox enqueue on a recorded event     proved in tests/integration/administrative_events_reach_the_bus_test.go
```

Every one of those tests exists and passes. The gap is *when* they run: a change that
deletes any of the four passes `make verify`, passes the pre-push hook, and passes
everything a contributor sees. Three of the four are fixes this remediation cycle added —
which means the newest guarantees are the least defended in the place defence is cheapest.

`TestTheEnforcementPointsAreStillCalled` is the cheap half of the answer: a structural
guard, in the gate, asserting each call is still present in the function that must have it,
naming the guarantee and the integration test that proves it works. It cannot say the check
is *correct*. It can say somebody removed it, which is what nothing did before.

---

## AFTER THE FIXES

The same twelve mutations, re-run:

```text
12 of 12 caught, baseline green
```

Five of them now fail in `tests/security`, which the gate runs.

---

## FULL RE-RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| integration, full suite | **PASS** |
| race over unit + security + integration | **PASS** |
| chaos, 9 scenarios | **PASS** |
| process, real binaries | **PASS** |
| mutation sweep, 12 mutations | **12 caught** |

---

## TWO NOTES ON METHOD

**The harness deleted my own fix once.** `revert()` runs `git checkout -- internal/`, which
discards uncommitted work, and my `RevokeGrantHandler` change was uncommitted. The test that
had just passed started failing and the fix was gone. It cost twenty minutes and it is the
second time this repository has recorded that hazard; the rule is to commit before running
anything that reverts.

**The first attempt measured nothing.** Twelve rows of "inconclusive", because Smart App
Control blocked every mutated binary on the host and retrying an identical file is futile —
the verdict is per hash. Then a dozen rows of "did not compile", because `shell=True` on
this host is `cmd.exe`, which does not understand a `VAR=value` prefix, so every container
run failed and the harness reported it as a compilation failure. Both are the same class of
defect the audits keep finding, one level up: a harness that reports something other than
what it measured.

---

## OPEN RISKS

1. **The structural guard is not a behavioural one.** It knows a call is present, not that
   the check behind it is right. If those four checks need to be *correct* inside the gate,
   the gate needs infrastructure — which is a deployment decision, not a test one.
2. **The sweep covers twelve points, not every one.** Choosing them was a judgement, and a
   guarantee nobody thought to mutate is exactly the shape of what this pass found.
3. **Revocation now requires the issuer privilege** — see A-7-03, and check your deployment.
4. The connection budget is undocumented (sixth audit).
5. Retention is per tenant from the credential registry, and runs only in-process.
6. `authority_usage` grows without bound; no endpoint lists registered keys; the console has
   no human reviewer; the capacity envelope is one host.

---

## WHAT THIS PASS IS WORTH

The previous six asked whether the platform does what it says. This one asked whether the
tests would notice if it stopped, and the answer for five of twelve was no — including the
promise the project puts first.

It is also the cheapest audit of the seven to repeat: the mutation list is a JSON file and
the sweep is one command. A guarantee worth stating in a report is worth a row in it.

**V0 remains not self-accepted.** External audit of `afa4f45` is still what would settle it.
