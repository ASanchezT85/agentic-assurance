# Operations

## Local development

```sh
make bootstrap    # go modules, pnpm workspace, and the .venv for python tools
make up           # start postgres, clickhouse, redis, nats, spire; wait for health
make migrate      # apply PostgreSQL migrations
make verify       # the quality gate
make test-integration
make down
```

## Database roles

Migrations run as `POSTGRES_USER`, which is a superuser: they create roles and
policies. The application connects as **`assurance_app`**, which is deliberately
`NOSUPERUSER NOBYPASSRLS`.

That distinction is load-bearing rather than tidy. PostgreSQL exempts superusers from
row level security entirely, so an application connecting as the superuser would make
every tenant-isolation policy inert while all the tests still passed. `POSTGRES_APP_DSN`
in `.env.example` is the application connection.

On a host without `make`: `scripts/bootstrap.sh` and `scripts/verify.sh` do the same.

## Which python

`scripts/python-bin.sh` resolves it, and both the Makefile and `verify.sh` defer to it:
an explicit `$PY`, then `.venv`, then whatever is on `PATH`.

The project venv comes first on purpose. Installing the Python tools globally puts
them in the user site-packages, and not every environment exposes that directory --
a GUI git client running the pre-push hook is one that does not. The symptom is
`No module named ruff` from an interpreter that clearly has ruff installed. `verify.sh`
preflights the three modules and tells you to bootstrap instead of failing with a
traceback.

## Where the gate runs

Three places, all running the same commands:

| Where | Entry point |
|---|---|
| CI | `.github/workflows/ci.yml`, five jobs |
| Local, by hand | `make verify` or `scripts/verify.sh` |
| Local, on push | `scripts/githooks/pre-push` |

The hook is opt-in per clone, because git does not version `.git/hooks`:

```sh
git config core.hooksPath scripts/githooks
```

Enable it when GitHub Actions is not available on the account hosting the repository.
Without either, nothing enforces the gate and it becomes documentation.

## Ports

| Service | Port | Notes |
|---|---|---|
| PostgreSQL | 5432 | bound to 127.0.0.1 |
| ClickHouse | 8123 (HTTP), 9000 (native) | bound to 127.0.0.1 |
| Redis | 6379 | persistence disabled on purpose |
| NATS | 4222 (client), 8222 (monitoring) | JetStream enabled |
| SPIRE server | none published | reachable inside the compose network only |

## Migrations

`make migrate` applies PostgreSQL and then ClickHouse.

ClickHouse migrations are **one statement per file**, applied in filename order. Its
HTTP interface takes a single statement per request, and splitting a multi-statement
file in shell is the kind of parsing that works until a semicolon appears inside a
comment.
| assurance-gateway | 8080 | `/healthz`, `/readyz` |
| fleet-engine | 8081 | `/healthz`, `/readyz` |

All container ports bind to loopback. Nothing in this compose file is safe to expose.

## Credentials

`.env.example` holds development-only, non-secret values and is the only committed env
file. Production credentials come from a customer secret manager, KMS, or a
Vault-compatible system (§35). Secrets are never logged, returned through an API,
embedded in evidence, or placed in telemetry payloads.

## Observability

OpenTelemetry traces, metrics and operational logs arrive with the code they
instrument. Operational logs are not audit evidence (INV-013, §51).

## Authoring and shipping a policy

```text
edit YAML  ->  Validate  ->  Compile  ->  Sign  ->  Simulate  ->  Shadow  ->  Canary  ->  Active
```

The pipeline is forward-only. Rollback is available from `SIGN` onward, is always
audited with a reason, and is terminal: a rolled-back bundle is never resurrected,
a new version is created instead (spec section 43).

Only `ACTIVE` enforces production. `SHADOW` and `CANARY` do not, and `Bundle.Enforcing()`
is the single place that answers the question.

Policy fixtures live in `tests/fixtures/policies/`. The spec's own example from
section 15.1 is one of them, so it is proven to compile and to behave as written.

## Known local quirk: Windows Application Control

On this workstation, Windows Application Control intermittently refuses to execute a
freshly built Go test binary:

```text
fork/exec ...\go-build.../pkg.test.exe: An Application Control policy has blocked this file.
```

It has nothing to do with the code. It is **intermittent, not deterministic**: the
same command on the same path fails and then passes on a retry, which is consistent
with a scanner holding a lock on a newly written executable rather than with any
path being disallowed. Re-running the test is usually enough.

`GOTMPDIR` moves where the binaries are built and sometimes helps, but it is not a
fix:

```sh
export GOTMPDIR=/some/path
```

`verify.sh` deliberately does not set it. A repository-local `.gotmp` would drop build
artifacts exactly where the structure and scope guards walk, which breaks them for a
real reason while trying to work around an unrelated one. If this becomes frequent
enough to matter, the fix is an exclusion in the host's security policy, not a change
in this repository.

## Evidence is not logs

They are different systems with different rules, and INV-013 exists because treating
them as interchangeable is easy and quiet.

| | Operational logs | Evidence |
|---|---|---|
| Purpose | a human debugging a process | the account of a financial decision |
| Destination | stdout, then whatever collects it | `evidence_events` in PostgreSQL |
| Ordering | best effort | ordered by `occurred_at`, then `sequence` |
| Completeness | sampled and droppable under pressure | complete or the timeline is wrong |
| Mutability | rotated and deleted freely | append-only; corrections are new rows |
| Queryable by | whoever runs the log tool | `GET /v1/evidence`, tenant-scoped |
| Retention | operational choice | bounded windows only, never selective edits |

Rotating logs aggressively is fine. Deleting evidence is not, and the application role
cannot: it holds `SELECT` and `INSERT` on that table and nothing else.

Secrets belong in neither (spec section 35).

## Market data (optional)

`ALPHAVANTAGE_API_KEY` enables the participation component of the Fleet Risk Vector.
Without it, `P` is UNKNOWN, which is the correct outcome and not a degraded one
(ADR-019).

**The free plan does not make P usable for real cohorts.** It serves daily totals;
intraday is a paid endpoint, and the key is capped at 25 requests per day. The adapter
refuses any window shorter than a session rather than prorating a daily volume across
a minute, because intraday volume is heavily weighted to the open and the close and a
prorated denominator would look precise while being wrong in both directions.

So today the adapter answers session-length questions and the fleet engine asks
minute-length ones. Closing that needs an intraday feed.

Verified live on 2026-08-27: AAPL, 10,198,442,317 of notional.

```sh
ALPHAVANTAGE_API_KEY=... go test -tags=integration -run Live ./tests/integration/
```

The test skips without a key and spends at most one request.

## Credentials in this repository

There are none, and a guard says so. `TestNoAPIKeyValuesAreCommitted` scans every
committed file for an assignment whose value looks like a real key.

Values that are deliberately written down because they are worthless outside a
laptop end in **`_dev_only`** (`assurance_dev_only`, `assurance_app_dev_only`). That
is a naming convention the guard recognises, so the intent lives in the value itself
and anyone adding one has to say so in the name.

Everything else belongs in the environment or a secret manager.

## Benchmarks and chaos (Phase 15)

```sh
make up && make migrate
go test -tags=integration -count=1 -v ./tests/performance/
make test-chaos
```

The chaos suite **stops real containers**, which is why it has its own build tag
rather than riding on `integration`. Go runs test packages in parallel, so a broad
`-tags=integration ./tests/...` would take PostgreSQL down while the integration
suite was using it and both would hang. That is not a flake; it is two suites
disagreeing about who owns the infrastructure.

It restores every container through `t.Cleanup`, including on failure, but it will
interrupt anything else using the compose stack while it runs. Run it alone.

### Measured on the reference machine, 2026-08-28

| | p50 | p95 | p99 | Section 50.1 target |
|---|---|---|---|---|
| Enforcement path | 11.6 µs | 17.6 µs | 22.6 µs | 2 ms / 5 ms / 10 ms |
| Idempotency claim | 3.34 ms | 4.23 ms | 5.25 ms | — |

The enforcement path is envelope decode and validation, authority evaluation and
policy evaluation: about **0.6% of the p50 budget**.

**The idempotency claim exceeds the whole p50 budget on its own**, at 3.34 ms against
a 2 ms target. ADR-015 predicted this would be the largest single item and named the
remedy in advance: batching or a local write-ahead log, never moving idempotency truth
into Redis. This is one PostgreSQL round trip per claim against a container on the
same laptop, so the number describes this machine rather than a deployment — but the
shape of the finding does not change with better hardware, because the ratio between
23 µs of computation and a database round trip does not.

### A measurement defect worth remembering

The first version of the gateway benchmark timed one evaluation per sample and
reported **p50 = 0s, p95 = 0s**. That was not a fast path, it was the clock: Windows
timer granularity is coarser than a single evaluation, so most samples rounded to
zero and the percentiles described the timer. Each sample now times a batch of 200 and
divides.

### The race detector does not run here

`-race` needs cgo, and this Windows host has no gcc. CI runs it on
`internal/...`, `tests/security/...` and `tests/scenarios/...`, which is where the
concurrency guarantees live. Locally the concurrency tests still run and still assert
their outcomes; they just cannot detect a data race that happens not to change one.
