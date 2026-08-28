# API documentation

The public V0 surface is deliberately small (spec §46), plus the two read-only
evidence endpoints added by ADR-023.

```text
POST /v1/intents                        Phase 1-5
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

POST /v1/simulations                    Phase 11
GET  /v1/simulations/{id}               Phase 11
```

Every mutation endpoint requires an authenticated tenant, an authorized actor, a
correlation id, and produces an audit event.

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
