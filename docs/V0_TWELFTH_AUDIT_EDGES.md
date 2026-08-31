# V0 TWELFTH AUDIT — THE EDGES THE CENSUS EXCLUDED

**Build audited:** `2a54835` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** the eleventh audit's surface census, pointed at the trees it left out —
`adapters/` and `cmd/` — and then a read of what the unexercised methods actually do.

The eleventh ended with *"the census covers `internal/`. `cmd/`, the adapters and the console
have their own surfaces and are not in it."* This is that sentence, carried out.

---

## STATUS

```text
A-12-01  HIGH   the Alpaca adapter discarded parse errors on the execution path: an
                order that filled could be recorded as having filled nothing
A-12-02  INFO   the broker interface is five methods wider than the platform uses
```

A-12-01 is fixed and verified red. A-12-02 is a design decision for the owner, not a defect,
and is reported rather than acted on.

---

## THE MEASUREMENT

```text
37 exported functions and methods in adapters/ and cmd/
28 executed by a test that runs without infrastructure
 9 never executed
```

Far better than `internal/` (199 of 337), and the nine are concentrated: the read side of the
two broker adapters — `CancelOrder`, `GetOrder`, `GetOrders`, `GetPositions` — plus the
object store, which needs MinIO.

Reading what those unexercised methods do is where the finding came from. A census tells you
where nobody has looked; it does not tell you what is there.

---

## A-12-01 — THE ADAPTER THAT SAYS IT REFUSES WHAT IT CANNOT MAP, GUESSED

**Severity: HIGH. INV-012, on the execution path.**

`adapters/alpaca` converts the venue's order into the platform's canonical one. The function
that does it opens with:

> *"toBrokerOrder converts a venue order into the canonical one, refusing anything it cannot
> map rather than guessing (INV-012)."*

Two lines below that comment:

```go
filled, _ := strconv.ParseFloat(w.FilledQty, 64)
avg, _   := strconv.ParseFloat(w.FilledAvgPrice, 64)
```

A `filled_qty` the adapter cannot parse becomes **zero**. Not an error, not an unknown
outcome: a filled quantity of zero, written into the evidence chain, returned to the caller,
and settled against the customer's authority. An order that filled, recorded as having
filled nothing — by the adapter whose contract is that a venue's failure cannot corrupt the
core model.

Four more of the same in the read path: `qty` and `avg_entry_price` on positions, `cash` and
`buying_power` on the account. A venue that answers `"n/a"` for buying power produces an
account with no buying power, stated as fact.

This is on `Submit` and on `Reconcile` — both go through `toBrokerOrder` — so it is the path
every order takes, not a reporting nicety.

### Why nobody saw it

Alpaca sends `""` for `filled_avg_price` until something fills. `ParseFloat("")` errors, the
error is discarded, the value is zero — **which is the correct answer for that case**. The
discard looked harmless because the one case anybody exercised was the one where guessing
happens to be right.

### The fix

`venueNumber(field, raw)`: an empty string is zero and nothing else is; anything else that
does not parse is an error naming the field and the value. Applied to all six.

### Verified red

Putting the two discards back makes `TestAVenueNumberThatIsNotANumberIsRefused` fail with
*"the venue sent an unparseable filled_qty and the adapter accepted it"*.

### The read side, now exercised

`GetPositions` and `GetAccount` had never been executed by any test. They now are — including
the assertion that the adapter leaves `InstrumentID` empty rather than inventing a canonical
identity from a venue symbol, which is §13's ticker-is-not-an-identity rule and was
previously only a comment.

**Tradier is clean**: it decodes numbers as JSON numbers, so a malformed value fails at the
decoder. **Alpha Vantage is clean**: it keeps both parse errors and refuses the quote.

---

## A-12-02 — THE INTERFACE IS FIVE METHODS WIDER THAN THE PLATFORM USES

**Severity: informational. A decision, not a defect.**

`broker.Adapter` requires seven methods. The platform calls two:

```text
called by the platform     SubmitOrder, Reconcile   (plus Capabilities)
called by nothing outside
the adapters and the
contract tests             CancelOrder, GetOrder, GetOrders, GetPositions, GetAccount
```

Every venue integration must therefore implement, and ship, five live-API paths the platform
never exercises. That is a cost imposed on each new adapter and a surface that reaches
production without a caller to shake it out — A-12-01 lived in exactly that surface.

`CancelOrder` is the one worth stating plainly: **the platform never cancels an order at the
venue.** That is consistent with what the product says it does — §1 is explicit that it
contains order flow *"before it reaches execution infrastructure"* — and no document claims
otherwise. It is not a contradiction; it is a shape worth knowing, because "we can stop it"
means "we can refuse the next one and revoke the authority", not "we can pull back the one
that is working".

Two honest options: narrow the interface to what the platform requires and make the rest an
optional extension, or keep it and write down why. Both are the owner's call; an audit that
refactored the venue contract on its own would be doing product design under another name.

---

## WHAT WAS RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| `go test ./adapters/... ./tests/contract/` | **PASS** |
| A-12-01 verified red before the fix | yes |
| surface census over `adapters/` and `cmd/` | 28 of 37 executed |

**NOT RUN:** integration, race, chaos, process — fifth audit running. Docker Desktop is
stopped on this host and starting it needs elevation. The object store's three unexecuted
methods sit behind that.

---

## OPEN RISKS

1. **The full matrix, five audits deep.** Unchanged and unchanging until the daemon is up.
2. **The console has no census.** `apps/console-web` is a third surface, and its behaviour
   tests need the stack, so it stays unexamined by this method for now.
3. **Adapter read-side coverage is now Alpaca-only.** Tradier's `GetPositions` and
   `CancelOrder` remain unexecuted; the same discard class does not exist there, but the
   request shaping is unchecked.
4. **Positions and accounts are `float64`** while the order path is exact decimal. Deliberate
   in scope — they are read-side — but the boundary between the two representations is not
   documented anywhere.
5. Everything carried from the sixth through eleventh audits.

---

## WHAT THIS PASS IS WORTH

A-12-01 is the second-most serious finding of the twelve, after the release path that had
never worked: it is on the execution path, it corrupts the canonical model with a plausible
value, and it sat two lines under a comment promising the opposite.

The method that found it was not clever. The eleventh audit's census said *"nobody has ever
run these"*, and this pass read them. That order — measure where nobody looked, then look —
is the whole of it, and it is the cheapest of the twelve to repeat.

**V0 remains not self-accepted.**
