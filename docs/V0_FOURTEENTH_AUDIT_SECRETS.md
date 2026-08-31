# V0 FOURTEENTH AUDIT — THE PROMISES, AND THE ONE THAT WAS BROKEN EVERY BAD AFTERNOON

**Build audited:** `b274396` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** the platform's absolute claims — *never*, *cannot*, *must not* — taken as
testable assertions rather than as documentation, starting with the class where being
wrong is a breach rather than a bug.

Thirteen audits have asked what the code does. This one asks whether the code's own
sentences are true.

---

## STATUS

```text
A-14-01  HIGH   the analytical store's password rode in a URL query string, and Go's
                *url.Error prints the URL, so three log sites wrote it in plaintext
```

One defect, fixed at the source, **red observed** before the fix. The other five
credential-bearing values in the platform are handled correctly, and one of them is
handled correctly by an adapter that clearly knew about this exact trap.

---

## THE MEASUREMENT

```text
366 absolute claims in comments across internal/, adapters/ and cmd/
  6 credential-bearing values in the platform
  5 that keep the section 35 promise
  1 that did not
```

Section 35 is the sharpest promise the platform makes, and it is four promises in one: a
store or broker credential is **never in plaintext, never logged, never returned through an
API, never in evidence or telemetry.** Four sinks, each one checkable.

---

## A-14-01 — A SECRET IN A URL IS A SECRET IN EVERY LOG THAT URL TOUCHES

**Severity: HIGH. Section 35, and it fired on the ordinary failure path.**

`internal/fleet/clickhouse.go` authenticated by query string:

```go
params.Set("user", s.User)
params.Set("password", s.Password)
```

Go's `*url.Error` — what `http.Client.Do` returns — prints the URL it failed on. `net/http`
strips a password out of *userinfo* (`https://user:pass@host`); it does not touch the query
string. So the client's own error read:

```text
clickhouse unreachable: Post "http://…/?database=assurance&password=hunter2_dev_only&query=SELECT+1&user=u": dial tcp …
```

and three call sites log that error verbatim:

```text
internal/gateway/telemetry.go:142   "intent telemetry not written",     "err", err
internal/gateway/telemetry.go:151   "dependency telemetry not written", "err", err
internal/fleet/producer.go:170      "fleet measurement failed",         "err", err
```

**An outage is precisely when those lines run.** This was not an edge case reachable by a
malformed input; it was the ordinary behaviour of a bad afternoon, and the log it wrote to
is the one an operator ships to a log aggregator.

`internal/fleet/api.go` is the near miss and worth naming: it takes the same error and
writes a generic 503 without logging it. One of four call sites happened to be careful.

### The fix

The credential travels in `X-ClickHouse-User` / `X-ClickHouse-Key`, ClickHouse's own headers,
and leaves the URL entirely.

Moving the secret rather than scrubbing the message, deliberately. A query string leaks in
more places than a Go error: a reverse proxy's access log, an exporter's URL label, and
ClickHouse's own `system.query_log` all keep it, and none of them are reachable from a
redaction helper in this repo. Nothing can redact a secret out of a log it has already been
written to.

### Red observed

Before the fix, a probe pointed at a closed port printed the password in the error text and
failed. That is the first red check verified in three audits — the two tests that now guard
it (`TestTheStoreCredentialIsNeverInAnErrorMessage`, and one asserting the credential
arrives in the headers so that moving it did not simply mean losing it) pass with the fix.

---

## THE OTHER FIVE, AND WHY ONE OF THEM MATTERS MOST

```text
adapters/alpaca         APCA-API-SECRET-KEY header                    correct
adapters/tradier        Authorization: Bearer header                  correct
adapters/objectstore    SecretKey only inside the SigV4 HMAC          correct
identity.Credentials    never formatted, never logged                 correct
adapters/marketdata     apikey in the query string — vendor-mandated  accepted, and safe
```

The market-data adapter is the one to sit with. Alpha Vantage has no header form, so the key
must go in the URL — and **every error in that file is hand-written with no `%w` of the
transport error.** Its only two `%w` uses wrap its own sentinels. Somebody wrote that adapter
knowing exactly what wrapping a transport error would do to a key in a query string.

So the knowledge existed in this repository. It was applied where the secret belonged to a
vendor and not where the secret belonged to the platform.

---

## WHAT THE ROUND-TRIP PROBE FOUND FIRST — NOTHING, AND THAT IS WORTH RECORDING

This audit began somewhere else: after the thirteenth found a type that changed across a
wire, the obvious next question was whether any value changes crossing any other wire.

It does not, and the reasons are good ones already written down:

- `money.Amount`/`Quantity` marshal as literal decimal text and parse from the literal,
  never through `binary64`.
- `canonicaljson` decodes with `UseNumber`, keeps number literals verbatim, and its
  `default:` branch **refuses** any Go type it was not given — so a caller who built a map
  with raw `float64` gets an error instead of a different hash.
- Every type assertion on an evidence payload in the codebase is `.(string)`. Not one asserts
  a numeric type, so the JSONB `int64 → float64` trap has nothing to catch.
- All eight console collections are described in the OpenAPI document, so the collection-key
  guard has no unguarded endpoint to skip.

A null result on a whole method, reported rather than quietly replaced with the method that
worked. The pivot to the promise census came after this, not instead of it.

---

## WHAT WAS RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` | **PASS** |
| A-14-01 red observed before the fix | **yes** — the password printed in the error |
| the two credential tests after the fix | **PASS** |
| credential census across `internal/`, `adapters/`, `cmd/` | 6 found, 5 already correct |
| whole-struct logging sweep (`%+v`, `%#v`, `DumpRequest`, config logging) | none found |

**NOT RUN:** integration, race, chaos, process — **seventh audit running**. Docker Desktop is
stopped on this host and starting it needs elevation.

---

## OPEN RISKS

1. **The full matrix, seven audits deep.** Environmental, unchanged, and now longer-standing
   than any technical item in the record by a wide margin.
2. **Nothing enforces the class.** Two tests guard the ClickHouse client specifically. No
   check stops the next credential from being put in a URL, and the census that found this
   one was run by hand.
3. **Rotation.** The password that has been running on any real deployment has been in that
   deployment's logs. The fix stops new leaks; it does not un-write old ones, and the
   operator's answer to this finding is to rotate, not to upgrade.
4. **360 of the 366 absolute claims are unexamined.** This pass checked one class because it
   was the highest-stakes one, not because the others are safe.
5. **The console still has no behavioural test** (carried from the thirteenth): nothing
   asserts that an unavailable source renders `Unavailable` rather than zeros. All six pages
   do it correctly today, by reading.
6. Everything carried from the sixth through thirteenth audits.

---

## WHAT THIS PASS IS WORTH

The most serious finding since the release path that had never worked, and the cheapest to
have found: the platform wrote the promise down, in a specification section with a number,
and no one had ever pointed a test at the sentence.

The pattern across fourteen passes is now hard to miss. Every method that reads a document
finds what the document knows. Every method that runs the system finds what the documents do
not. This one did something slightly different — it read the platform's own sentences **as
tests** — and the first sentence it checked was false on the ordinary failure path.

**V0 remains not self-accepted.**
