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

**Run 2026-08-29, green in 27 s**: enforcement survives ClickHouse, NATS, Redis and the
whole intelligence plane going away; PostgreSQL going away fails closed; a missing
policy bundle denies; a broker timeout does not duplicate; a gateway restart loses
nothing that matters.

One gap worth naming rather than leaving to be discovered: the suite drives
`execution.Service` directly, so the control stage added later is not in it. That an
unreadable control store denies is proved by a unit test that injects the failure
(`CONTROL_UNAVAILABLE`), not by stopping a container.

### Measured on the reference machine, 2026-08-28

| | p50 | p95 | p99 | Section 50.1 target |
|---|---|---|---|---|
| Enforcement path | 12.5 µs | 16.8 µs | 20.5 µs | 2 ms / 5 ms / 10 ms |
| Idempotency claim | 3.34 ms | 4.23 ms | 5.25 ms | — |

Neither row is the cost of an intent. See "What an accepted intent actually costs"
below: the two together are a fraction of it, and reading them as the whole is what hid
six transactions of evidence for as long as it did.

The enforcement path is envelope decode and validation, authority evaluation and
policy evaluation: about **0.6% of the p50 budget**.

**The idempotency claim exceeds the whole p50 budget on its own**, at 3.34 ms against
a 2 ms target. ADR-015 predicted this would be the largest single item and named the
remedy in advance: batching or a local write-ahead log, never moving idempotency truth
into Redis. This is one PostgreSQL round trip per claim against a container on the
same laptop, so the number describes this machine rather than a deployment — but the
shape of the finding does not change with better hardware, because the ratio between
23 µs of computation and a database round trip does not.

### A thousand agents, measured 2026-08-28

Spec section 56 item 1 and section 66 step 2 ask for 1,000+ synthetic agents sending
concurrent intents. `tests/performance/fleet_load_test.go`, behind the `load` build
tag, launches them against a running gateway:

```sh
GATEWAY_URL=http://localhost:8073 LOAD_AGENT_TOKEN=... LOAD_ISSUER_TOKEN=...   go test -tags=load -count=1 -v ./tests/performance/ -run TestAThousandAgents
```

| | Result |
|---|---|
| Agents | 1,000, each with its own authority grant |
| Submissions | 3,000, all released at once |
| Decisions | 3,000 of 3,000; no 5xx, no unanswered request |
| Throughput | 407 intents/s |
| End-to-end over HTTP | p50 2.37 s, p95 2.49 s, p99 2.51 s |

One grant per agent rather than one shared: a thousand agents under a single rolling
limit would measure how fast a limit is reached, which is a different item.

The latencies are queueing, not computation — the enforcement path itself is 12.5 µs.
Three thousand requests arriving simultaneously at a laptop wait their turn, and the
number to read here is that every one of them got a decision.

**The run found a real default.** pgxpool sizes itself at four connections per CPU, and
with a thousand concurrent agents that is where every submission queues: 232 intents/s
on the default against 422/s with fifty connections, same hardware, same enforcement
work. Both binaries default to 50 (`POSTGRES_MAX_CONNS`) through `internal/pg`, and a
DSN that sets `pool_max_conns` itself is left alone. It lived in the gateway's main for
a while and the fleet engine kept the library default — a sizing policy that applies to
one of two processes is not a policy, it is a patch.

Two things the harness had to be told, both properties of this machine rather than of
the platform. A thousand simultaneous TCP connections overflow the Windows listen
backlog and the kernel answers with RST, which arrives at the client as "connection
refused" and looks exactly like a platform that stopped deciding; sockets are capped at
256 and the agents queue on them. And `GOTMPDIR` has to point inside the repository —
Application Control on this host blocks freshly built test binaries under
`AppData\Local\Temp`.

### The listing query, measured against a day of traffic

`GET /v1/intents` ranked envelopes by last activity, which meant summarising the window
to fill one page: against 917,000 events the plan grouped **909,061 rows into 177,087
aggregates** and kept fifty, at 450–880 ms. Ranking by arrival makes it a bounded index
scan — **5–35 ms** on the same data, with the index from migration 0015.

Worth keeping as a pattern rather than a fix: the query was correct from the first day
and only wrong at volume, and the load runs are what produced the volume to see it in.

### Sustained, and across tenants

Two more runs behind the same `load` tag. `make test-load` covers the burst; these are
`-run TestSustainedLoad` and `-run TestTenantsUnderLoad`.

**Sustained, 2 minutes, 50 workers each with its own grant:**

| Minute | Decided | p50 | p99 | Codes |
|---|---|---|---|---|
| 0 | 23,037 | 125 ms | 221 ms | ACCEPTED 23,037 |
| 1 | 28,332 | 124 ms | 171 ms | ACCEPTED 18,613, ROLLING_LIMIT_EXCEEDED 9,719 |

51,419 decisions, **428/s sustained**, no 5xx and no unanswered request.

The refusals are the finding worth keeping. Each grant allows 1,000,000 of rolling
hourly notional and each order is 1,200, so a worker gets 833 orders an hour and this
run spent that in about ninety seconds. The platform did not slow down or fall over: it
kept deciding at the same latency and started saying no, which is the behaviour a
rolling limit is for. The run reports decision codes as well as statuses for exactly
this reason — a wall of 403s nobody can attribute reads as a platform falling over.

**Across tenants:** two tenants submitting concurrently, each with its own credential,
grant and signed policy bundle. Every intent each one lists is its own. That check is
not about speed: a pooled connection carrying a stale `app.tenant_id` is a failure
concurrency produces and a quiet test never sees, and INV-007 had only ever been proved
against an idle database.

### Alpaca Paper, the one check that needs a real venue

Spec section 66 step 7 asks for valid orders sent to Alpaca Paper. Everything about the
adapter is covered against a stub — request shape, status mapping, ambiguous timeouts,
credentials never surfacing — and a stub cannot answer the only question a live run
answers: whether the request we build is the request Alpaca accepts.

```sh
ALPACA_BASE_URL=https://paper-api.alpaca.markets ALPACA_KEY_ID=... ALPACA_SECRET_KEY=... make test-live
```

It places a limit order far below the market so it rests rather than fills, reconciles
it by client order id — the half that matters for an ambiguous outcome, because the id
has to be one Alpaca actually stored — and cancels it, so a run leaves no position and
no resting order behind. Without credentials it skips loudly: a green run that proved
nothing is worse than a skipped one.

The adapter refuses a non-paper endpoint at construction, so a live URL in that
variable fails before the first order rather than after it.

**Run 2026-08-29, and it found a real defect on the first attempt.** The adapter took
the venue symbol from its injected mapping instead of from `OrderRequest.Symbol`, which
the platform had already resolved. In the running gateway that mapping was a
passthrough of the canonical instrument id, so the order asked Alpaca for an asset
called `instr_us_equity_00206R102` and came back rejected. Every unit test injected a
real mapping and the fake broker accepts any symbol, so nothing but an order at a real
venue could show it.

With the fix, the whole section 66 chain runs against Alpaca Paper: envelope → identity
→ authority → policy → venue, order `dd6481bf-abce-41f6-883a-87ff4def9a70` accepted, the
evidence chain complete, and the resting order cancelled afterwards — the account is
left with no open orders and no positions.

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

## Serving the submission path

`POST /v1/intents` is served only when the enforcement plane is fully configured. Each
of these is load-bearing, and the gateway logs which one is missing rather than
answering anyway:

| Variable | Without it |
|----------|-----------|
| `POSTGRES_APP_DSN` | Idempotency and consumed usage have nowhere authoritative to live. |
| `GATEWAY_API_CREDENTIALS` | Nothing authenticates a caller. `identity@tenant=token,identity@tenant=token`; a credential without a tenant and tokens under 32 characters are refused at startup (ADR-025). |
| `POLICY_PUBLIC_KEY` | A policy bundle cannot be verified, and an unverified bundle is not policy. Hex-encoded ed25519 public key. |
| `POLICY_BUNDLE_DIR` | Where signed bundles live, one JSON file per tenant. Default `/etc/assurance/policy`. |
| `INSTRUMENT_SYMBOLS` | No instrument maps to a venue symbol. Default `/etc/assurance/instruments.json`. |
| `BROKER` | No venue. `alpaca` or `tradier`; both refuse a non-paper endpoint themselves. `fake` is a deterministic venue for development and additionally requires `ASSURANCE_ENV=development`, because a production gateway pointed at a fake venue accepts every order and sends none: it would look healthy while nothing it authorized ever reached a market. |

Optional, and their absence is a stated degradation rather than a failure:

| Variable | Without it |
|----------|-----------|
| `SPIFFE_TRUST_BUNDLE`, `SPIFFE_TRUST_DOMAIN` | An SVID cannot be verified, so callers reach A1 at best. Reported, not silently treated as attestation. |
| `SPIFFE_WORKLOADS` | A verified SVID establishes a workload and no customer, and the request is refused naming the missing entry. `spiffe://domain/path=tenant`; a path ending in `/` matches everything beneath it. |

Both binaries read the same three settings, so a workload certificate reaches A2 on
every surface or on none. Having it work on the submission endpoint and silently not on
the ones that read a customer's own data is an inconsistency an operator discovers from
a 401 they cannot explain.

**A caller may present both a certificate and a bearer credential.** A verified SVID
wins; when it does not verify — a service mesh certificate arriving at a service that
does not speak SPIFFE, for instance — the credential is used and the response records
why the certificate was rejected. Presenting a certificate does not cost a caller its
credential.

The gateway will not sign or activate a policy bundle. One that reaches it must
already be ACTIVE and signed: a gateway that activated its own policy would be
deciding what constrains it, which is the shape INV-009 exists to forbid.

**The usage ledger is per grant and lives in PostgreSQL.** `MemoryUsage` exists for
tests and single-process runs and is wrong for several replicas: two gateways each
enforcing half a rolling limit enforce no rolling limit at all.

### Running the submission path locally

```
make up && make migrate
export POSTGRES_APP_DSN=postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable
export GATEWAY_API_CREDENTIALS='svc_local=<a token of at least 32 characters>'
export POLICY_PUBLIC_KEY=<hex ed25519 public key>
export POLICY_BUNDLE_DIR=./local/policy INSTRUMENT_SYMBOLS=./local/instruments.json
export BROKER=fake ASSURANCE_ENV=development
go run ./cmd/assurance-gateway
```

The startup log says which route is served. If anything above is missing the endpoint
is **absent rather than failing**: `POST /v1/intents` answers 404, `/healthz` still
answers 200, and a WARN names the missing setting and its consequence. A gateway that
accepted intents it could not evaluate would be worse than one that does not accept
them at all.

## The fleet engine

The engine measures closed windows of stored intents and serves the read-only
intelligence API. It recommends and never enforces: there is no code in it that can
submit an order or change a customer's policy (INV-009).

Two halves have to be configured, and each is silent about a fleet it cannot see.

**The gateway feeds it.** With `CLICKHOUSE_HTTP_URL` set, every *decided* intent is
written to the analytical store asynchronously, in batches, off the enforcement path.
Denied intents are written too: a fleet view built from executions alone cannot see a
cohort that is being refused, and "forty agents all hit the same limit in the same
minute" is the signal the engine exists to surface. Without the variable the gateway
warns and the engine measures an empty fleet.

**The engine measures it.**

| Variable | Meaning |
|----------|---------|
| `FLEET_COHORT_TENANTS` | Comma-separated tenants to measure. Without it nothing is measured and the API serves only what is already stored. |
| `FLEET_WINDOW` | Window width and tick. Default `1m`. |
| `FLEET_LAG` | How long a window stays open before it is measured, because an intent decided at 14:59:59.9 can land after 15:00:00. Default `15s`. |

A measurement reports `intent_count` with `authorized_intents` and `refused_intents`
beside it. The flow figures cover every decided intent, refused ones included, because
the fleet vector measures *intent*. The split is what stops a reader taking a gross
notional for what actually reached a market.

## The simulation API

Served by the fleet engine. Absent rather than failing when it cannot run anything, and
the startup log names what is missing.

| Variable | Without it |
|----------|-----------|
| `POSTGRES_APP_DSN` | A simulation nobody can retrieve is a log line. |
| `INTELLIGENCE_API_CREDENTIALS` | Nothing authenticates a caller on **either** the fleet endpoints or the simulation endpoints, and both carry tenant data. Same format as the gateway's. Named for the plane rather than for simulations, because a variable called `SIMULATION_*` invited an operator to leave it unset while serving a customer's risk posture to anyone. |
| `SIMULATOR_PYTHON` | There is no engine to run. The project interpreter is `.venv/Scripts/python.exe` or `.venv/bin/python`. |
| `SIMULATOR_REPO` | Working directory for the engine, which is invoked as `-m simulator.engine`. Default `.` |
| `SIMULATOR_SCENARIO_DIR` | Which scenario files a caller may name. Default `simulator/scenarios`. |
| `SIMULATION_TIMEOUT` | Bounds one run. Default `5m`. |
| `SIMULATION_CONCURRENCY` | How many engines run at once. Default `2`. |
| `SIMULATION_WATCHDOG` | How often a running engine checks whether its run was cancelled on another replica. Default `2s`, and the upper bound on how long a cross-replica cancellation takes to free the slot. |

**The concurrency cap matters more than it looks.** A simulation is CPU-bound and this
process also serves the intelligence API. Without a cap, a burst of simulation requests
starves the reads operators depend on during an incident, which is exactly when they
are looking at them.

Scenario files live in `SIMULATOR_SCENARIO_DIR` and are named without the `.json`
suffix. A caller names a scenario; a caller never gives a path.

### Cancelling across replicas

Cancellation kills a process, and a process lives in one replica. A cancellation that
lands on a fleet engine that does not hold the run marks the row — the row is the
authority — and the replica that does hold it notices within one `SIMULATION_WATCHDOG`
interval and stops the engine.

Polling rather than LISTEN/NOTIFY or a message on the bus: the truth is already in the
row, so asking the row needs no second system to be up. The watchdog **fails open** —
a read error is ignored — because the cost of failing open is a late kill and the cost
of failing closed is a running simulation destroyed by an unrelated database blip.

The response to a cancellation says which of the two happened. `engine_stopped: true`
means the slot is free now; `engine_stopped: false` with `engine_stops_within` means it
comes back shortly.

### The tenant comes from the credential

Written `identity@tenant=token`. A caller is authenticated **for a tenant**, and a
request naming a different one is refused: `401 TENANT_NOT_AUTHENTICATED` on the
submission endpoint, `403` on the simulation API when a header disagrees.

A credential without a tenant is refused at startup rather than defaulted, because a
credential that only proves who is calling leaves the tenant to come from the request —
and every tenant-scoped lookup, row level security included, then uses whatever the
request said.

A caller that legitimately acts for several tenants needs several credentials.

### Broker credentials

Deliberately absent from `.env.example`, and `tests/scope_guard_test.go` refuses to let
them appear there: a committed example file with a slot for a venue secret invites
someone to paste a live one into the working tree. They belong in a secret manager
(spec section 35), and they are named here instead.

| Variable | Meaning |
|----------|---------|
| `ALPACA_BASE_URL` | The Alpaca endpoint. The adapter refuses anything that is not a paper endpoint, so a live URL here fails at construction rather than at the first order. |
| `ALPACA_KEY_ID`, `ALPACA_SECRET_KEY` | Alpaca Paper credentials. |
| `TRADIER_BASE_URL` | The Tradier endpoint. Sandbox only, refused the same way. |
| `TRADIER_TOKEN` | Tradier sandbox token. |
| `TRADIER_ACCOUNT_ID` | The account orders are placed against; Tradier scopes its paths by it. |

V0 implements no real-money path. Both adapters check this themselves rather than
trusting configuration, because a venue URL is a string and a mistake in it is the one
mistake nobody gets to take back.

## What the quality gate does not run

`make verify` runs vet, the linters, the type checkers, the unit and security and
scenario and contract suites, and the console build. It says so when it finishes,
because a gate that reports "passed" without naming what it skipped is read as covering
everything.

| Not in the gate | Why, and what it is where |
|-----------------|---------------------------|
| `make test-integration` | Needs real PostgreSQL, ClickHouse, NATS, Redis and SPIRE. Row level security, the append-only trigger, mutual TLS and the cross-replica kill are only real against real services. |
| `make test-chaos` | Stops those containers, so it cannot run alongside anything else. |
| `make test-race` | `-race` needs cgo, and this development environment has no C compiler. It runs in a container built from `scripts/race.Dockerfile`. |
| `make test-race-integration` | The same, over the integration suite against services on the host. This is where the concurrent idempotency claims, the cross-replica cancellation and the watchdog actually run. |

### The race detector

It had never run on this repository. Not a decision — an absence nobody had priced:
CI is disabled at the repository level and the development machine has no compiler. The
project already requires Docker, so the detector runs where a compiler exists.

The first clean report was worth very little. The detector only reports races on code
that actually runs concurrently while it watches, and seven of the nine structures with
a mutex or an atomic had no test that ran them from more than one goroutine. A clean
race report over sequential tests says the tests are sequential.
`tests/security/concurrency_test.go` exercises them, and removing a single lock from
the usage ledger produces four data races, which is how that suite was checked.

Nothing is excluded. The first version skipped `internal/simulation`, because its tests
execute the project's Python interpreter and a `.venv` built on Windows cannot run in a
Linux container. That removed from the detector the one package with two goroutines, a
mutex and an atomic — the cancellation path, the watchdog and the in-flight map — for a
reason that had nothing to do with concurrency, and removing the lock from its registry
then produced zero reported races.

The image carries an interpreter instead, and the helper that finds one checks that it
can import what the engine imports rather than that a file exists. Two weaker versions
of that check each hid something: the file test failed against a Windows `.venv`, and
the "does it start" test found the container's own `python3`, which starts and has no
numpy.

Over the integration suite the detector reports 43 passing and 13 skipped. The skips are
SPIRE, which needs a docker client the container does not have, and the live market data
tests, which need an API key.
