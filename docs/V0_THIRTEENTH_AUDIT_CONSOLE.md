# V0 THIRTEENTH AUDIT — THE CONSOLE, AND THE TYPE NOBODY CHECKED

**Build audited:** `ea19561` · **Build after remediation:** this commit · **Date:** 2026-08-30
**Method:** the third surface — `apps/console-web` — compared field by field against what the
backend actually serialises, and then compared by **type** rather than by name.

The eleventh audit's census covered `internal/`. The twelfth covered `adapters/` and `cmd/`
and closed on *"the console has no census."* This is that sentence, carried out.

---

## STATUS

```text
A-13-01  MEDIUM  every 64-bit count reached the console as a quoted string, and the one
                 surface that compares them ranked "9" above "10"
```

One defect, fixed at the source. The field **names** were clean in all eight collections —
which is exactly why this was still there.

---

## THE MEASUREMENT

```text
8 collections the console reads
8 whose field names match what the handler emits
1 whose field types do not
```

The name comparison was the obvious audit and it found nothing. The console declares
`intent_count: number`, the handler emits `intent_count`, and every check anybody had
written — including `tests/security/console_contract_test.go`, written by an earlier pass in
this same sequence — asks whether the field is *there*.

---

## A-13-01 — A COUNT IS NOT A NUMBER UNTIL SOMEBODY SAYS SO

**Severity: MEDIUM (intelligence plane).**

The three fleet-engine surfaces read ClickHouse through `JSONEachRow`, passed to the console
untouched. ClickHouse **quotes `UInt64` in JSON by default** — deliberately, because a value
past 2^53 cannot survive a JavaScript number. `internal/fleet/clickhouse.go` sent no settings
at all, so the server default stood, and every `count()`, `countIf()` and `uniqExact()` in
that file arrived as `"42"`.

Nothing downstream could report it:

```text
TypeScript          believes the declaration; the JSON is never validated against it
React               renders "42" and 42 identically
the contract test   asks whether `agents` exists, not what it is
```

So it surfaced in the one place that does arithmetic on them — `app/dependencies/page.tsx`,
picking the most widely shared dependency:

```tsx
(most, d) => (most === undefined || d.agents > most.agents ? d : most)
```

On two strings, `>` compares character by character: **`"9" > "10"` is true.** The
Dependencies surface exists to answer *"how many agents rest on one thing"*, and its
headline — the concentration, the finding the whole page is for — named the wrong dependency
whenever the leader had more digits than a rival. A dependency shared by 9 agents outranked
one shared by 10.

It is on the intelligence plane, so nothing was enforced on it (INV-009). What it corrupts is
a stated measurement, presented in the source strip as fact.

### The fix

One line in the ClickHouse client:

```go
params.Set("output_format_json_quote_64bit_integers", "0")
```

At the source rather than coerced in the console, and set explicitly rather than left to the
server default. The column list inside those SQL strings is the entire contract for three
surfaces; a console-side `Number()` would fix today's reader and leave the next one to find
this again.

### Red not verified, and that is a gap

Smart App Control on this host blocked every test binary built from the reverted source —
three attempts, while the fixed build runs green. So the test passes with the fix and I could
not watch it fail without it. Stated rather than skipped: an audit that claims a red check it
did not see is the failure these passes exist to catch.

### The trap, walked into again

`git checkout -- internal/fleet/clickhouse.go`, used to undo the red-check revert, destroyed
the fix along with it. That is the **fifth** time in this sequence, and it is now recorded in
the report rather than only in my own working notes.

---

## WHAT ELSE THE CENSUS FOUND, AND DID NOT

- **All eight console reads have a caller.** No orphan surfaces, unlike `internal/`.
- **`SURFACES` is still six** — the §48 list, matching the six directories under `app/`. No
  seventh has appeared.
- **No write path.** Nothing in `lib/api.ts` posts; the six pages render and nothing else.
- **`DateTime64` and `Decimal`** are declared `string` and `number` respectively on the
  console side and arrive that way. The notionals are `Float64`, so `.toFixed()` is safe —
  had they been `Decimal`, ClickHouse quotes those unconditionally and the Fleet page would
  have thrown on render rather than merely misreporting.

That last one is worth keeping: the same root cause, one column type away, is a crash instead
of a wrong number, and the crashing version is the *better* outcome.

---

## WHAT WAS RUN

| Item | Result |
|---|---|
| `scripts/verify.sh` (includes console lint, typecheck, `next build`) | **PASS** |
| `go test ./internal/fleet/` with the fix | **PASS** |
| the same test without the fix | **BLOCKED by Smart App Control** — not observed |
| field-name census, 8 console collections vs handlers | 8 of 8 match |

**NOT RUN:** integration, race, chaos, process — **sixth audit running**. Docker Desktop is
stopped on this host and starting it needs elevation.

---

## OPEN RISKS

1. **The full matrix, six audits deep.** Unchanged, environmental, and now the longest-
   standing item in the record by a wide margin.
2. **Nothing checks the console's declared types against live responses.** The contract test
   checks names; this pass fixed one type mismatch by reading, not by measuring. A type check
   belongs in `console_fields_*_test.go`, which needs the stack.
3. **The console has no behavioural test of any kind** — the six pages are exercised only by
   `next build`. Nothing asserts that an unavailable source renders `Unavailable` rather than
   zeros, which is the console's single most important promise.
4. **Red checks are unverifiable on this host** while Smart App Control blocks rebuilt
   binaries. The Linux container was the workaround and it is down.
5. Everything carried from the sixth through twelfth audits.

---

## WHAT THIS PASS IS WORTH

Moderate, and it is the shape of the finding that matters more than its severity. Twelve
audits have compared names to names — refusal codes, invariants, field keys, exported
symbols. Every one of those methods would have passed this file. The defect lived in the gap
between a name that matched and a **type** that nobody on either side of the wire had ever
asserted, in a language that trusts the declaration and a database that documents the
opposite.

The console was the last unexamined surface, and it took twelve passes to look at it because
it enforces nothing. It also happens to be the only part of this platform a customer sees.

**V0 remains not self-accepted.**
