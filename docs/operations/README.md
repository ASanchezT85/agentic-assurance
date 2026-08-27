# Operations

## Local development

```sh
make bootstrap    # go modules, pnpm workspace, and the .venv for python tools
make up           # start postgres, clickhouse, redis, nats; wait for health
make verify       # the Phase 0 quality gate
make test-integration
make down
```

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
