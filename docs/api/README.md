# API documentation

The public V0 surface is deliberately small (spec §46), plus the two read-only
evidence endpoints added by ADR-023.

```text
POST /v1/intents                        DONE (internal/gateway, ADR-025)
GET  /v1/intents/{id}                   DONE (internal/gateway)
GET  /v1/intents/{id}/evidence          Phase 6   DONE (ADR-023)
GET  /v1/evidence?correlation_id={id}   Phase 6   DONE (ADR-023)

POST /v1/authority-grants               Phase 3
POST /v1/authority-grants/{id}/revoke   DONE (internal/gateway)

GET  /v1/fleet/state                    Phase 14  DONE (fleet-engine)
GET  /v1/cohorts                        Phase 14  DONE (fleet-engine)
GET  /v1/dependencies                   Phase 14  DONE (fleet-engine, added for section 48.3)

GET  /v1/incidents                      Phase 10
GET  /v1/incidents/{id}                 Phase 10

POST /v1/controls                       Phase 13

POST /v1/simulations                    DONE (internal/simulation)
GET  /v1/simulations/{id}               DONE (internal/simulation)
```

**Every endpoint that carries tenant data authenticates**, not only the mutating ones.
The tenant comes from the credential (`identity@tenant=token`), never from a header or
a body field, and a header that disagrees is `403` rather than ignored. There is no
unauthenticated mode: with no credential registry configured, every such endpoint
refuses.

That was not true until an audit went looking. `GET /v1/evidence` returned a tenant's
whole audit chain and `GET /v1/fleet/state` returned its risk posture, both to anyone
who named the tenant in a header. Each carried an honest comment saying a header is not
authentication and that the real thing would arrive with the surface that carried it.
It did not arrive, and the comments made the gap look handled.

Mutation endpoints additionally require an authorized actor and a correlation id, and
produce an audit event.

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
curl -H "Authorization: Bearer $GATEWAY_TOKEN" \
  'http://localhost:8080/v1/evidence?correlation_id=corr_1'

curl -H "Authorization: Bearer $GATEWAY_TOKEN" \
  'http://localhost:8080/v1/intents/env_1/evidence'
```

Both are read-only and tenant-scoped. The chain is returned exactly as stored:
corrections appear as later events referencing earlier ones and are never merged
away, because a reader needs to see that a correction happened rather than a tidied
result.

**The tenant comes from the credential.** It used to come from a header, and this
paragraph used to say so and promise that authentication would arrive with the surface
that carried it. It arrived — and the paragraph did not change, so for two audit passes
it told a reader these endpoints were unauthenticated and safe only behind network
isolation. A stale caveat is worse than none: it describes a system nobody is running.

## Intelligence API (Phase 14)

Served by **fleet-engine**, read-only, and tenant-scoped by the caller's credential.

```sh
curl -H "Authorization: Bearer $INTELLIGENCE_TOKEN" http://localhost:8081/v1/fleet/state
curl -H "Authorization: Bearer $INTELLIGENCE_TOKEN" http://localhost:8081/v1/cohorts
curl -H "Authorization: Bearer $INTELLIGENCE_TOKEN" http://localhost:8081/v1/dependencies
```

There is no handler in `internal/fleet` that writes anything. That is true of the
package and not of the process: the fleet engine also serves the simulation endpoints,
which create and cancel simulation runs. What bounds the plane is imports rather than
this sentence — it cannot reach a policy bundle, an authority grant, an idempotency
record or a venue, and `tests/security/INV-009_intelligence_plane_test.go` checks that
against the binary's whole dependency closure.

`/v1/dependencies` is not in the section 46 list. It was added because section 48.3
requires a Dependencies surface and there was no endpoint behind it; it is a read-only
projection of `dependency_observations` and is recorded here rather than left as an
undocumented addition.

**The tenant comes from the credential**, and a header that disagrees with it is 403
rather than ignored. What these endpoints return is a customer's risk posture, and
naming a tenant in a header was enough to read all of it until an audit went looking.

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

A run moves QUEUED → RUNNING → COMPLETED, FAILED or CANCELLED. A completed run carries the
engine's record whole, with `result_fingerprint` and `scenario_source_hash`: the second
is a sha256 of the scenario file's exact bytes, so a record says *which file* was run
rather than only what it was called. A failed one carries the engine's own reason.

`GET /v1/simulations` lists a tenant's runs newest-first and **omits the records** —
fifty of them is megabytes of detail nobody asked for.

A run that belongs to another tenant returns 404, identical to one that never existed.
Spec section 45 lists cross-tenant leakage as a threat, and an error that tells the two
apart is itself the disclosure.

## POST /v1/simulations/{id}/cancel

```json
{ "cancelled_by": "ops@example" }
```

`cancelled_by` is required, for the same reason the submission records who asked:
humans are audited too (spec section 36), and "why did this run stop" should have an
answer six months later.

| Status | Meaning |
|--------|---------|
| 200 | Stopped. The body carries `cancelled_at`, `cancelled_by` and `engine_stopped`. |
| 400 | No `cancelled_by`. |
| 404 | No such run — and identically, a run belonging to another tenant. |
| 409 | The run had already finished. |

**409 rather than a silent 200**, because a caller told "cancelled" about a run that
had already completed would think a result they still have was thrown away.

A run can be cancelled while it is still QUEUED, not only while RUNNING. On a busy
engine that is where a run spends most of its life, and a cancellation that did nothing
in that window would appear to work and then let the run start a moment later.

CANCELLED is terminal and is deliberately **not** FAILED: nothing went wrong, and a
failure count that included cancellations would make the engine look unreliable every
time someone changed their mind. An engine that finishes just after the cancellation
lands has its result discarded — the operator was told it was stopped, and a record
appearing afterwards would make that a lie.

`engine_stopped` says whether the process was killed on the spot, which happens when
the cancellation reaches the replica holding it. When it does not, the replica that
does is watching the row and stops it within one watchdog interval;
`engine_stops_within` says how long that is.

Both are reported rather than collapsed into "cancelled", because an operator
cancelling a run to free capacity for a different one needs to know whether the slot is
free now or shortly. Measured against two live replicas: the slot came back **860 ms**
after a cancellation that landed on the wrong one, on a run with twenty-seven seconds
left to go.

## GET /v1/intents/{id}

The status of a submitted intent, by its envelope id. It closes the caller's loop:
submission returned an outcome and there was no way to ask again, so a caller that lost
the response had only the evidence chain, which answers a different question in a shape
meant for an auditor.

A run that is still `PENDING` carries no `outcome` field, and that absence is the
answer: the platform has claimed the key and does not yet know what the venue did.

Building it turned up that nothing enforced spec section 12.2's one-intent-per-envelope
rule. Two submissions carrying the same envelope id under different idempotency keys
produced two orders for one stated intention — found when the unique index refused to
build over twenty-five rows a test suite had created by reusing a fixed id. The index
enforces it now, and the refusal is `ENVELOPE_REUSED` rather than a constraint error.

## POST /v1/authority-grants/{id}/revoke

```json
{ "revoked_by": "ops@example", "reason": "agent credential compromised" }
```

Both fields are required. This is the one action whose entire purpose is to be
explained afterwards, and a revocation without an actor and a reason is an operational
mystery six months later (spec section 36).

`authority.Store` has carried `Revoke` since Phase 3 and nothing exposed it, so cutting
an agent's authority meant an operator with a psql prompt — during exactly the incident
where that is the worst way to work.

Revoking an already-revoked grant is **200 with `already: true`**, not 409. Revocation
is the emergency action, and an operator hitting it twice under pressure should be told
it is done rather than handed an error to interpret.

It is served whenever the database is reachable, independently of whether a venue and a
policy bundle are configured. The lever must not depend on the submission path being
healthy: it is what an operator reaches for when it is not.
