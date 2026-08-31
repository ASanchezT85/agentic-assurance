# V0 NINTH AUDIT — THE PROMISES NOBODY WROTE DOWN

**Build audited:** `7ee8101` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** census of every refusal the code can return, measured by execution coverage.

The eighth audit ended on its own limit: *"the invariants are a list somebody wrote, and a
guarantee the platform relies on but never wrote down is still invisible to this method."*

So this pass takes no list written by a person. It takes the platform's own promises as the
code states them — **every refusal code it can return** — and asks of each: does any test
ever make it happen?

A refusal that has never been produced is a promise nobody has checked. It may name the
wrong field, carry the wrong code, or be a branch that cannot be reached at all.

---

## STATUS

```text
before   64 of 106 refusal codes executed by a test that runs without infrastructure
after    97 of 106
```

Thirty-three refusals went from never-executed to executed, in five new test files. The
nine that remain are classified in §4 — four need a database, one is a package-level
variable coverage cannot see, and two look unreachable, which is a finding of its own.

---

## 1. THE METHOD, AND ITS FIRST WRONG ANSWER

The first census grepped the tests for each code string and reported **102 of 154 untested**.
That number was wrong and I nearly wrote it down: a test that asserts on a sentinel error —
`errors.Is(err, ErrStalePredecessor)` — never mentions `ACTIVATION_STALE_PREDECESSOR`
anywhere. Grep measures vocabulary, not behaviour.

The second census asks the suite instead. Go's coverage profile records which statements ran;
the census maps each refusal to the line that produces it and reports the ones no test has
ever reached. It also filters to codes in a *refusal position* — the first pass counted
`US_OPEN`, `SAME_TENANT` and `PARTIALLY_FILLED` as untested refusals, which are enum values.

Both corrections are in the tool, in comments, because the failure mode they fix is the one
these audits keep finding: an instrument that reports something other than what it measured.

`tools/refusals/census.py`, alongside the mutation sweep.

---

## 2. A-9-01 — THE SIGNED ACTIVATION DOCUMENTS HAD NO VALIDATION TESTS

**Severity: MEDIUM-HIGH. The most security-critical document type in the platform.**

`Authorization` is the customer's signed statement that a policy bundle may enforce.
`KeyAuthorization` is the customer's signed statement that another key may make that
statement. Between them, `Validate`, `Verify` and `Authorizes` can refuse in **28 distinct
ways** — schema, tenant, bundle, hash, action, actor, time, nonce, algorithm, key id,
signature, predecessor, self-signature, an empty validity window, a private key pasted where
a public one belongs.

Not one of those branches had ever been executed by a test.

They were not forgotten out of carelessness. The integration tests drive these documents
through a real database and a real gateway — the happy path, a tamper case, a replay — so
`Validate` runs constantly and *returns nil*. Every refusal inside it was dead code as far
as the suite was concerned. Which means: if `ACTIVATION_ACTOR_MISSING` named the wrong
field, or `ACTIVATION_HASH_MISSING` had been unreachable since it was written, nothing would
have said so.

`internal/policy/activation_refusals_test.go` now exercises all of them, plus the four
`Authorizes` mismatches — tenant, bundle id, content hash, no bundle — and the three
`ActivationKey.Usable` refusals.

The content-hash case is the one worth naming: an authorization whose bundle id matches and
whose content hash does not is a customer approving one set of rules and a different set
arriving. That refusal existed and had never fired in a test.

---

## 3. A-9-02 — SEVEN ENVELOPE REFUSALS AT THE BOUNDARY EVERY ORDER CROSSES

**Severity: MEDIUM.**

`intent.Validate` is the first thing an order meets. Seven of its refusals had never been
produced: `ORDER_TYPE_INVALID`, `STOP_PRICE_REQUIRED`, `STOP_PRICE_NOT_ALLOWED`,
`LIMIT_PRICE_REQUIRED`, `PRICE_NOT_POSITIVE`, `TIME_IN_FORCE_INVALID`,
`DEPENDENCY_TYPE_INVALID`.

`internal/intent/refusal_codes_test.go` produces each one and asserts the **field** it names
as well as the code, because a refusal that points a caller at the wrong field costs a round
trip and reads as a platform fault.

Three more were added in the same pass: `SIGNATURE_MISSING` (identity),
`RESERVATION_KEY_REUSED` on the in-memory ledger and `ENVELOPE_ABSENT` (authority), and
`NO_CONTROL_APPLIES` (control) — the last being the code carried by every order on an
ordinary day, which no test had ever read.

---

## 4. THE NINE THAT REMAIN, AND WHY

| Code | Why it is not executed here |
|---|---|
| `ACTIVATION_STALE_PREDECESSOR` | in `ActivationStore`, needs PostgreSQL — covered by `tests/integration/policy_transition_cas_test.go` |
| `ACTIVATION_PREDECESSOR_MISSING` | same |
| `ACTIVATION_KEY_LAST_USABLE` | same — covered by `tests/integration/activation_key_hardening_test.go` |
| `ACTIVATION_KEY_UNKNOWN` | same |
| `NO_CONTROL_APPLIES` | a package-level `var`, not a statement; coverage cannot see it. The new test does execute it |
| `SVID_INVALID` | reachable, needs a malformed certificate fixture |
| `UNSUPPORTED_ORDER` | reachable, needs a broker whose capabilities exclude the order type |
| `ACTIVATION_CANONICAL_FAILED` | **appears unreachable**: it fires only if canonicalising a struct that has already marshalled fails |
| `INSTRUMENT_NORMALIZATION_FAILED` | **appears unreachable**: the normalizer classifies every malformed id with a code of its own, so the fallback never fires. A ticker is refused as `INSTRUMENT_ID_IS_A_TICKER` |

The last two are worth stating rather than testing. A refusal that cannot happen is not
dangerous, and it is not free either: it is a branch a reader trusts, a code a client is
told to handle, and a line that will be maintained for ever by people who assume it fires.
They are left in place and named here; removing them is a decision for someone who can
confirm the normalizer contract cannot change.

---

## 5. WHAT WAS RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| `go test ./internal/... ./tests/...` with coverage | **PASS** |
| refusal census, before | 64 of 106 executed |
| refusal census, after | **97 of 106** |

**NOT RUN.** Integration, race, chaos and process, for the same reason as the eighth audit:
Docker Desktop is stopped on this host and starting it needs elevation. The four
database-bound refusals in §4 are covered by tests I cannot execute here, and I am not
calling that PASS.

---

## 6. OPEN RISKS

1. **The full matrix is still owed**, and now for two audits running.
2. **Two refusals look unreachable** and are still shipped as if they were not.
3. **The census covers `internal/`.** Refusals produced in `cmd/` or in the console are not
   in it, and the console has its own vocabulary of unavailability.
4. **Coverage is not correctness.** A refusal executed once by a table-driven test is a
   refusal that fires; whether it fires in the *right* circumstance is what the behavioural
   suites are for.
5. Everything carried from the sixth, seventh and eighth: connection budget undocumented,
   retention per credential-registry tenant and in-process only, `authority_usage` unbounded,
   no endpoint lists registered keys, console unreviewed by a human, one-host capacity.

---

## 7. WHAT THIS PASS IS WORTH

Eight audits looked at what the platform promises in documents people wrote — a spec, a
list of invariants, a set of ADRs. This one read the promises out of the code, and found
that the most security-critical document type in the system had twenty-eight refusal
branches and no test had ever taken one.

The pattern across the last four audits is now hard to miss. Every method that started from
a written artefact found what that artefact happened to mention; every method that started
from the running system or the code itself found something the documents did not know about.
This is the third of those, and the cheapest to repeat: two commands, both in `tools/`.

**V0 remains not self-accepted.**
