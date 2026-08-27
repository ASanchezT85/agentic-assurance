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
