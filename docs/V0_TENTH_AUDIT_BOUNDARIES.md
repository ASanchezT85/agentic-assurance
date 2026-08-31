# V0 TENTH AUDIT — THE EDGES, AND THE THIRD COPY OF ONE RULE

**Build audited:** `f06f3b1` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** every comparison that decides money or time, examined and then tested at the
exact boundary; and every place the same quantity is computed more than once, compared.

Nine audits have asked what the platform does. This one asks where it does it *by one
unit*: the `>` that should be `>=`, the window that should be half-open, the two components
that compute the same amount and disagree at the extreme. Nothing in the suite sat on a
boundary — every example was comfortably inside its limit — so a flipped operator would
have changed no test at all.

---

## STATUS

```text
A-10-01  MEDIUM  intent.ClusterNotional read an unrepresentable order as a determinate
                 zero, while the two other implementations of the same rule call it
                 indeterminate
```

One defect, fixed, verified red. Fourteen boundary cases pinned that nothing had pinned
before — and, unusually for these audits, the conventions themselves all held.

---

## A-10-01 — THREE COPIES OF ONE RULE, TWO OF THEM RIGHT

**Severity: MEDIUM (intelligence plane).**

"What does this intent commit" is computed in three packages:

```text
authority.EffectiveNotional   decides whether the grant allows it
policy.effectiveNotional      compares it against the customer's thresholds
intent.ClusterNotional        counts it toward a parent intention
```

Each comment says it mirrors the others. Two of them do.

`money.NotionalOf` multiplies price by quantity in `big.Int` and returns **zero** when the
product does not fit the platform's representation. Its own comment says why, and what the
caller owes:

> *"Returning a wrapped value would be worse than any refusal: it would be a small number
> standing in for an enormous order. The caller treats zero as indeterminate and denies."*

`authority` and `policy` do exactly that. `ClusterNotional` returned `(0, true)` —
determinately zero — so the largest order the platform can ever see counted as contributing
**nothing** to the analysis that looks for one intention split into many. The order most
worth noticing was the one invisible to the heuristic built to notice it.

It is on the intelligence side, so no money moved wrongly: INV-009 keeps fleet analysis out
of enforcement. What it corrupts is the fleet measurement and the cluster/fragmentation
signal — `internal/fleet/measure.go` and `internal/fleet/clickhouse.go` are its callers.

**Fixed**, and verified red: reverting the guard fails
`TestTheThreeNotionalRulesAgree/a_product_too_large_to_represent` with
`ClusterNotional says (0.0000, true) and authority says (0.0000, false)`.

The test pins the *agreement* rather than the arithmetic: seven intents through all three
implementations, so a fourth copy or a drift in one of the three fails here rather than
surfacing as a fleet number nobody can reconcile.

---

## THE BOUNDARIES, NOW PINNED

The conventions were already consistent everywhere I looked — five implementations of the
validity window, four of the notional ceiling, two of the rolling window in Go and SQL. That
is a good result and it was untested, which made it a good result nobody could rely on.

```text
a grant is valid over [valid_from, valid_until)     inclusive start, exclusive end
an agent key, an activation key, a control          the same, in all four places
a money limit is a ceiling that may be reached      1000 permits exactly 1000
an open-order count may not be reached              max 2 permits two, refuses the third
a limit of zero means no limit                      not "permit nothing"
the rolling window excludes its far edge            an entry exactly 1h old is outside
policy notional_gt / _gte / _lt / _lte              exactly what the words say, at 1000
require_notional_lte is satisfied at its ceiling    1000 satisfies "at most 1000"
```

Fourteen cases across `internal/authority/boundary_test.go` and
`internal/policy/threshold_boundary_test.go`, each at the exact value and each with one
unit either side where that distinguishes anything.

The asymmetry between the money limits and the open-order count is the one worth naming: a
notional ceiling is inclusive and a count is exclusive, so `per_order_notional: 1000` allows
an order of 1000 while `max_open_orders: 2` refuses the third. Both readings are the natural
English of the setting, and they use opposite operators — which is exactly the kind of thing
a future edit unifies "for consistency" and breaks.

---

## A PROBE THAT WAS WRONG, AND WHAT IT FOUND ANYWAY

The first version of the agreement test asked whether policy "saw an amount", and reported
four failures. Policy was right and the probe was wrong: when a size-dependent rule meets an
order whose size cannot be determined, a DENY rule **fires** and an ALLOW rule does not —
fail-closed, deliberate, and stated in the source.

That behaviour was itself only implicitly covered, so the corrected test now pins it. And
the wrong probe is recorded here for the same reason the last three audits recorded their
harness faults: an audit that reports its own bad instrument as a finding is the failure
these passes exist to catch.

---

## WHAT WAS RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| the new boundary and agreement tests | **PASS** (agreement verified red first) |
| `go test ./internal/... ./tests/...` | **PASS** |

**NOT RUN**, for the third audit running: integration, race, chaos, process. Docker Desktop
is stopped on this host and starting it needs elevation I do not have. Nothing in this pass
touches a database path, which is why it was chosen while the daemon is down — but the
matrix is still owed.

---

## OPEN RISKS

1. **The full matrix is owed**, three audits deep now. This is the largest standing gap in
   the record, and it is environmental rather than technical.
2. **`money.Amount.Add` is unchecked int64 addition.** Two values each at the parser's
   maximum sum to one unit past `MaxInt64`. Reaching it requires a grant with ~461 trillion
   already consumed and an order of ~461 trillion, so it is a theoretical edge rather than a
   practical one — but the guard that bounds parsing does not quite bound addition, and that
   is now written down rather than assumed.
3. **The boundary census covered money and time.** Sequence numbers, counts, string lengths
   and rate limits have their own edges and were not examined.
4. Everything carried from the sixth through ninth audits.

---

## WHAT THIS PASS IS WORTH

Less than the sixth or the ninth, and it is worth saying so: one MEDIUM defect on the
intelligence side, and a set of conventions that turned out to be right. A pass that finds
the code correct is not a wasted pass — the fourteen cases are now the thing that fails when
somebody unifies two operators "for consistency" — but it is a smaller result than the ones
that found a release path that had never worked or twenty-eight refusals nobody had ever
produced.

The method's own limit is visible too: I chose which comparisons to look at. A boundary in
code I did not read is exactly as invisible as an invariant nobody wrote down.

**V0 remains not self-accepted.**
