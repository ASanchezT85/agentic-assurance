# Envelope fixtures

Versioned test data for the `AgentExecutionEnvelope` contract (spec section 60:
"test fixtures committed with versioning").

```text
valid/     envelopes that MUST validate
invalid/   envelopes that MUST be rejected
```

Every file in `invalid/` declares the codes it must produce, in a sibling file with
the same name and a `.codes` extension, one stable code per line. A fixture that
fails for the wrong reason is not a passing test, so the harness asserts the codes
rather than merely asserting failure.

`internal/intent/fixtures_test.go` walks both directories. Adding a fixture adds a
case; there is no list to update.

Codes are stable API. Renaming one is a breaking change for anyone who built
error handling on it.
