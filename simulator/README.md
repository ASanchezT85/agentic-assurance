# Digital Twin simulator

## Running a scenario

```bash
python -m simulator.engine --scenario demo --seed 42
python -m simulator.engine --scenario simulator/scenarios/correlated_panic.json --seed 7 -o run.json
```

`--seed` is required. An unseeded run is not reproducible, and a run nobody can
reproduce is an anecdote.

Scenarios are JSON files. Until now the engine could run exactly one, hardcoded, and a
new question meant editing Python. A file is validated strictly and **refused rather
than repaired**: a misspelled `panic_probabilty` produces a population that never
panics, a plausible result, and a fingerprint that reproduces perfectly, so the
experiment is wrong and everything about it looks right.

The record carries `result_fingerprint` and `scenario_source_hash`. The second is a
sha256 of the scenario file's exact bytes, so a record says *which file* was run rather
than only what it was called: two files sharing a `scenario_id` are different
experiments.

## What the twin is not

`simulator/assurance/engine.py` is not the production policy engine. That lives in Go,
in `internal/policy` and `internal/authority`, and it is the only thing that decides
anything real. The twin applies a deliberately small set of deterministic limits so a
scenario can observe what changes when enforcement is present.

The two share exactly one rule: the per-order notional limit. Both read
`tests/fixtures/per_order_limit_cases.json`, checked from Python in
`simulator/test_engine_agreement.py` and from Go in
`tests/security/twin_agreement_test.go`. If they ever drift apart, one of those fails
and names the case. Everything else about the twin's rules can differ from production
and nothing will tell you, which is why scenarios that assert on production policy live
in `tests/scenarios/` against the real engines instead.
