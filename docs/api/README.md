# API documentation

The public V0 surface is deliberately small (spec §46), plus the two read-only
evidence endpoints added by ADR-023.

```text
POST /v1/intents                        DONE (internal/gateway, ADR-025)
GET  /v1/intents/{id}                   Phase 1-5
GET  /v1/intents/{id}/evidence          Phase 6   DONE (ADR-023)
GET  /v1/evidence?correlation_id={id}   Phase 6   DONE (ADR-023)

POST /v1/authority-grants               Phase 3
POST /v1/authority-grants/{id}/revoke   Phase 3

GET  /v1/fleet/state                    Phase 14  DONE (fleet-engine)
GET  /v1/cohorts                        Phase 14  DONE (fleet-engine)
GET  /v1/dependencies                   Phase 14  DONE (fleet-engine, added for section 48.3)

GET  /v1/incidents                      Phase 10
GET  /v1/incidents/{id}                 Phase 10

POST /v1/controls                       Phase 13

POST /v1/simulations                    DONE (internal/simulation)
GET  /v1/simulations/{id}               DONE (internal/simulation)
```

Every mutation endpoint requires an authenticated tenant, an authorized actor, a
correlation id, and produces an audit event.

## POST /v1/intents

The enforcement plane's only write path. It runs an envelope through every check in
`docs/architecture/hot-path.md`, in that order, and an order reaches a venue only if
all of them allow it.

Authentication is an X509-SVID from the transport (reaching A2) or a bearer credential
from the configured registry (reaching A1). Nothing else is executable (ADR-025).

Status codes carry the decision, so a client that reads only the status code cannot
mistake a refusal for an acceptance:

| Status | Meaning |
|--------|---------|
| 200 | The order reached the venue. |
| 202 | The outcome is unresolved. The platform accepted the intent and does not yet know what the venue did (INV-004). Not a failure. |
| 400 | The envelope did not validate. `details` carries the validation codes. |
| 401 | Nothing authenticated the caller, or the envelope claimed more attestation than the transport established. |
| 403 | Authority or hard policy refused. `stage` names which. |
| 413 | The envelope exceeds the accepted size. |
| 422 | The intent could not be executed, for instance because no venue symbol exists for the instrument. |

Every response names the `stage` that decided and a stable `code`, and a policy
decision carries the bundle id and content hash that produced it: a decision without
its bundle is an assertion that some policy, once, said no (ADR-010).

The endpoint is **absent, not failing**, when the enforcement plane is not fully
configured. A gateway with no signed policy bundle, no venue and no credentials
answers 404 here rather than accepting intents it cannot evaluate.

OpenAPI is generated from the canonical schemas in `packages/` rather than
hand-written (§60). Nothing is generated in Phase 0 because no endpoint exists yet.

## Evidence endpoints (Phase 6)

```sh
curl -H 'X-Tenant-Id: tenant_acme' \
  'http://localhost:8080/v1/evidence?correlation_id=corr_1'

curl -H 'X-Tenant-Id: tenant_acme' \
  'http://localhost:8080/v1/intents/env_1/evidence'
```

Both are read-only and tenant-scoped. The chain is returned exactly as stored:
corrections appear as later events referencing earlier ones and are never merged
away, because a reader needs to see that a correction happened rather than a tidied
result.

**The tenant comes from a header, and that is not authentication.** Spec section 46
requires an authenticated tenant on every endpoint; that arrives with the API surface
that carries authentication. Until then these endpoints are reachable only inside the
customer's own network, and the handler makes no check it does not perform.

## Intelligence API (Phase 14)

Served by **fleet-engine**, read-only, tenant-scoped by header.

```sh
curl -H 'X-Tenant-Id: tenant_acme' http://localhost:8081/v1/fleet/state
curl -H 'X-Tenant-Id: tenant_acme' http://localhost:8081/v1/cohorts
curl -H 'X-Tenant-Id: tenant_acme' http://localhost:8081/v1/dependencies
```

There is no handler here that writes anything, which is not a convention: spec section
29 forbids the fleet engine from submitting orders or modifying customer policy, and
an API with no write path cannot be talked into one.

`/v1/dependencies` is not in the section 46 list. It was added because section 48.3
requires a Dependencies surface and there was no endpoint behind it; it is a read-only
projection of `dependency_observations` and is recorded here rather than left as an
undocumented addition.

**The tenant is a header and that is not authentication.** Section 46 requires an
authenticated tenant on every endpoint. That arrives with the API surface that carries
authentication; until then these are reachable only inside the customer's network, and
the handler makes no check it does not perform.

The tenant is validated as identifier-shaped rather than escaped. ClickHouse's HTTP
interface has no parameter binding in the form this client uses, and an identifier
that is not identifier-shaped is a request to refuse rather than to sanitise.

## POST /v1/simulations and GET /v1/simulations/{id}

Served by the **fleet engine**, not by a fifth deployable: a simulation is
intelligence and not enforcement, and ADR-011 counts four. POST is the only mutating
endpoint in that process, and what it mutates is the simulation's own record. Nothing
in `internal/simulation` writes a policy bundle, an authority grant or an order, which
is INV-009 expressed as an absence rather than as a check.

```json
POST /v1/simulations
{ "scenario": "correlated_panic", "seed": 7, "requested_by": "ana@example" }
```

`seed` is required and has no default: an unseeded run is not reproducible, and a seed
the platform chose silently is one the caller cannot quote back. Unknown fields are
refused — a caller who wrote `seeed` would otherwise get a reproducible run of a seed
they did not choose, and every retry would return the same wrong answer.

`scenario` is a **name, never a path**. Letters, digits, underscore and dash, plus the
built-in `demo`. The name reaches a filesystem and then a process argument vector, so
anything else is refused rather than sanitised: stripping the bad characters would
honour a request the caller did not make. The engine is executed with an argument
vector and never through a shell, in a deliberately small environment that carries none
of the platform's credentials (spec section 35).

| Status | Meaning |
|--------|---------|
| 202 | Accepted and durable. It has not run yet; `Location` carries the run's URL. |
| 400 | Not a runnable request: no seed, no requester, an unknown field, or a scenario name that is not a name. |
| 404 | No scenario by that name — and, on GET, no such run *for this tenant*. |
| 413 | The request body exceeds the accepted size. |

A run moves QUEUED → RUNNING → COMPLETED or FAILED. A completed run carries the
engine's record whole, with `result_fingerprint` and `scenario_source_hash`: the second
is a sha256 of the scenario file's exact bytes, so a record says *which file* was run
rather than only what it was called. A failed one carries the engine's own reason.

`GET /v1/simulations` lists a tenant's runs newest-first and **omits the records** —
fifty of them is megabytes of detail nobody asked for.

A run that belongs to another tenant returns 404, identical to one that never existed.
Spec section 45 lists cross-tenant leakage as a threat, and an error that tells the two
apart is itself the disclosure.
