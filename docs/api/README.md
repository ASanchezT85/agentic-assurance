# API documentation

The public V0 surface is deliberately small (spec §46), plus the two read-only
evidence endpoints added by ADR-023.

```text
POST /v1/intents                        DONE (internal/gateway, ADR-025)
GET  /v1/intents                        DONE (internal/gateway)
GET  /v1/intents/{id}                   DONE (internal/gateway)
GET  /v1/intents/{id}/evidence          Phase 6   DONE (ADR-023)
GET  /v1/evidence?correlation_id={id}   Phase 6   DONE (ADR-023)

POST /v1/agent-keys                     DONE (internal/gateway)
POST /v1/agent-keys/revoke              DONE (internal/gateway)
POST /v1/policy-activation-keys         DONE (internal/gateway, ADR-028)
POST /v1/policy-activation-keys/revoke  DONE (internal/gateway, ADR-028)
POST /v1/authority-grants               DONE (internal/gateway)
POST /v1/authority-grants/{id}/revoke   DONE (internal/gateway)

GET  /v1/fleet/state                    Phase 14  DONE (fleet-engine)
GET  /v1/cohorts                        Phase 14  DONE (fleet-engine)
GET  /v1/dependencies                   Phase 14  DONE (fleet-engine, added for section 48.3)

GET  /v1/incidents                      DONE (internal/incident)
GET  /v1/incidents/{id}                 DONE (internal/incident)

POST /v1/controls                       DONE (internal/gateway + internal/control)
GET  /v1/controls                       DONE (internal/gateway)
POST /v1/controls/{id}/revoke           DONE (internal/gateway)

POST /v1/simulations                    DONE (internal/simulation)
GET  /v1/simulations                    DONE (internal/simulation)
GET  /v1/simulations/{id}               DONE (internal/simulation)
POST /v1/simulations/{id}/cancel        DONE (internal/simulation)
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
| 403 | Authority, a fleet control or hard policy refused, or the idempotency key belongs to another envelope. `stage` names which. |
| 413 | The envelope exceeds the accepted size. |
| 422 | The intent could not be executed, for instance because no venue symbol exists for the instrument. |

Every response names the `stage` that decided and a stable `code`, and a policy
decision carries the bundle id and content hash that produced it: a decision without
its bundle is an assertion that some policy, once, said no (ADR-010).

The endpoint is **absent, not failing**, when the enforcement plane is not fully
configured. A gateway with no signed policy bundle, no venue and no credentials
answers 404 here rather than accepting intents it cannot evaluate.

**OpenAPI is generated** into [`openapi.json`](openapi.json) by
`go run ./cmd/openapi-gen`, from the routes the binaries register and the canonical
schemas in `packages/` (§60). It has no route list of its own: the paths come from the
`HandleFunc` registrations, so an endpoint cannot be missing from it by omission, and a
served route with no description stops the generator rather than being published blank.

A generated file that is committed is wrong from the next endpoint onwards, so a test
regenerates it and compares. This file stays the prose — why each endpoint behaves as
it does — and that document stays the contract.

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

## POST /v1/controls

```json
{ "control_id": "ctl_1", "incident_id": "inc_1", "action": "READ_ONLY",
  "scope": "agent", "agent_id": "agent_1",
  "authorized_by": "riesgo@example", "policy_bundle_id": "bundle_v1",
  "reason": "correlated liquidation across the cohort",
  "expires_at": "2026-09-04T00:00:00Z" }
```

Where a fleet recommendation becomes something that binds. INV-009 is the rule — fleet
intelligence recommends, customer policy authorizes — and `internal/fleet` made it a
property of the types: `fleet.Authorize` is the only function that produces an
enforceable control and it needs an `Authorization` the intelligence plane cannot
construct. Nothing outside a test ever called it, so every recommendation the platform
made stopped at shadow mode for want of a surface rather than by decision.

**It is served by the gateway, not the fleet engine.** A control is enforcement. An
endpoint that let the intelligence plane produce one would break INV-009 by deployment
topology while every type signature still looked right.

The recommendation is read from the incident's evidence, never taken from the request.
A customer authorizes what the platform recommended; a body that could describe its own
recommendation would let an operator apply a control nothing suggested and leave a
record saying the platform proposed it. An incident with no `control.recommended.v1`
is `404`.

`scope` is stated: `tenant`, `agent`, `agents` (with `agent_ids`) or `account`, and one
kind only — a control naming an agent and an account at once reads as "both" or
"either" depending on the reader, and those differ by an outage. An omitted scope would
read as the whole tenant, which is the widest control there is and never something to
arrive at by leaving fields blank. `expires_at` is required: a control nobody has to
renew is one that throttles an agent forever because of an incident last spring.

**The platform does not expand a cohort into its members.** Four comments and this file
used to say the scope was "resolved to concrete agents and accounts at authorization
time"; nothing resolved anything, and ISOLATE_COHORT was an action whose name was a
lie — the choices were one agent or the whole customer. `agent_ids` is why the scope
exists at all now. The expansion stays manual on purpose: who is in a cohort is
measured over a rolling window, and an enforcement scope that changed as measurements
arrived would be a control nobody authorized.

Authorizing requires the same privilege as issuing authority (`GATEWAY_GRANT_ISSUERS`).
A credential that could both submit orders and authorize the controls over them is an
agent adjusting its own leash.

| Status | Meaning |
|--------|---------|
| 201 | Authorized and in force. |
| 400 | Not authorizable as written; `details` says why. |
| 403 | This credential may not authorize fleet controls. |
| 404 | No such incident for this tenant, or it carries no recommendation. |
| 503 | The incident evidence could not be read. Distinct from 404 on purpose: collapsing the two told an operator authorizing an emergency control during a database outage that the incident in front of them did not exist. |
| 409 | A control with this id already exists. |

**THROTTLE takes `max_orders` and `window_seconds`**, both required for that action and
refused on the others, where they would be numbers an operator believes are doing
something. It was refused outright for as long as nothing counted orders, which left an
operator watching a cohort misbehave able to isolate it or stop it dead and unable to
simply slow it down — the proportionate response, and so the one they reach for first.

A slot is taken inside one transaction behind an advisory lock on the control: two
callers that each read the count, saw room and then wrote would both pass, and a rate
limit that only approximately holds under load fails exactly when it matters. A replay
keeps the slot it already holds, so a duplicate cannot spend the window twice.

**The rate belongs to the control, not to each agent under it.** A throttle over five
named agents at ten per minute permits ten orders between them, which is what
"throttle this cohort" means; ten each is five controls. Said here because the other
reading is just as natural and the difference is a factor of five during an incident.

The counted window is pruned as it is consumed, two windows back. One row per allowed
order kept for good would grow with traffic while only the last few minutes are ever
read, and the count that enforces the limit would slow down as the control aged.

The slot is spent at the control stage, before policy. An order refused later still
counted, which errs toward throttling more — for a control authorized during an
incident, that is the direction to err in. Revoking a throttle forgets its window, so a
released scope does not carry a spent minute into the next incident.

Enforcement is a stage of the submission pipeline, between authority and policy, and it
reads PostgreSQL rather than the fleet engine: an enforcement check that had to ask the
intelligence plane who is in a cohort would fail closed every time the analytical plane
blinked (INV-005).

A refused order is `403` with stage `CONTROL` and a code naming the action:
`CONTROL_READ_ONLY`, `CONTROL_COHORT_ISOLATED`, `CONTROL_APPROVAL_REQUIRED` or
`CONTROL_THROTTLED`, which says the rate and what it counted. A scope carrying both a
throttle and a stop is told it is stopped: a stop decides first, whichever was
authorized first. The last
denies rather than parking the order — V0 has no approval queue, and an order held for
an approval nobody can give is one that silently never happened.

The refusal records `control.enforced.v1`, not `control.applied.v1`. Applying is what a
customer did once; enforcing is what the platform does on every order after, and
recording the second as the first made the incident timeline report a human action for
each order the control stopped.

## GET /v1/controls and POST /v1/controls/{id}/revoke

```json
POST /v1/controls/ctl_1/revoke
{ "revoked_by": "ops@example", "reason": "the incident is closed" }
```

`POST /v1/controls` landed without either of these, which is the same gap
`POST /v1/authority-grants/{id}/revoke` was built to close, reappearing one endpoint
later. The table had `revoked_at` and `revoked_by`, the catalog had
`control.revoked.v1`, and nothing wrote them. A tenant-wide `READ_ONLY` control refuses
every order in the tenant, so lifting one meant a psql prompt — during exactly the
incident where that is the worst way to work, and out of reach for an operator with no
database access.

Revoking takes the same privilege as authorizing, requires `revoked_by` and `reason`,
and answers **200 with `already: true`** on a second attempt rather than 409, for the
reason revoking a grant does: an operator hitting it twice under pressure should be
told it is done. A control belonging to another tenant is `404`, identical to one that
never existed.

`GET /v1/controls` lists every control a tenant has, in force or not, and computes
`in_force` rather than leaving a reader to compare an expiry against their own clock. A
refusal names a `control_id`, and until this existed nothing could turn that id into
what it was, who authorized it and when it ends. It is readable by any credential of
the tenant: which controls constrain you is your own posture, and an agent that cannot
see why it is being refused files a bug against the wrong system.

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

## GET /v1/intents

```sh
curl -H "Authorization: Bearer $GATEWAY_TOKEN"   'http://localhost:8080/v1/intents?hours=24&limit=50'
```

A tenant's recent intents, newest arrival first, built from evidence rather than from
the idempotency table.

Ranked by when each intent arrived rather than by its last event, and that is a
performance decision stated rather than hidden: ranking by last activity meant grouping
every event of every envelope in the window — measured at 909,061 rows into 177,087
groups to return fifty, about half a second against a day of real traffic. By arrival
it is a bounded index scan, 5–35 ms on the same data. That choice is the endpoint: the idempotency table holds intents
that reached a venue, so a list built from it would show what was accepted and silently
omit every refusal — the half of the record an assurance platform exists to keep.

Each entry carries the events its envelope produced and the fields those events
recorded: the authority code, the policy action, the control that refused, the broker
order id. Nothing is folded into a verdict of this endpoint's own, for the reason
`GET /v1/intents/{id}` reports a chain rather than a summary.

`since` and `as_of` are in the response. The window defaults to 24 hours and the page
to 50 envelopes, and both are stated rather than implied: a list that quietly covered
"some of the past" reads as "everything", which is what makes an empty page look like a
quiet fleet.

## GET /v1/intents/{id}

The status of a submitted intent, by its envelope id. It closes the caller's loop:
submission returned an outcome and there was no way to ask again, so a caller that lost
the response had only the evidence chain, which answers a different question in a shape
meant for an auditor.

A run that is still `PENDING` carries no `outcome` field, and that absence is the
answer: the platform has claimed the key and does not yet know what the venue did.

**An intent that was refused answers here too**, with `state: NOT_EXECUTED` and the
evidence chain that stopped it. It used to answer 404: the idempotency record is
claimed when an order is submitted to a venue, and identity, authority, a fleet control
and policy all refuse before that, so a caller who lost the 403 was told their intent
never arrived — the exact question this endpoint exists to answer, answered wrongly.
The chain is reported rather than summarised into a verdict of this endpoint's own: a
summary that can disagree with the record is one that eventually does, and then the
record is right (ADR-009).

An envelope refused at validation is still 404, and correctly: nothing established an
envelope id to ask about, and the refusal named the fields in its own response.

**One key, one intent, both ways.** A key presented with a different envelope is
`IDEMPOTENCY_KEY_REUSED` rather than the earlier order's outcome. That direction was
open until an audit sent the same key with a different quantity and was told, with a
`200`, that its order had been accepted and filled — by an order it never placed. A
retry of the same envelope under the same key is still a replay, which is what
idempotency is for.

Building it turned up that nothing enforced spec section 12.2's one-intent-per-envelope
rule. Two submissions carrying the same envelope id under different idempotency keys
produced two orders for one stated intention — found when the unique index refused to
build over twenty-five rows a test suite had created by reusing a fixed id. The index
enforces it now, and the refusal is `ENVELOPE_REUSED` rather than a constraint error.

## POST /v1/authority-grants

```json
{ "grant_id": "grant_1", "principal_id": "prin_1", "account_id": "acct_1",
  "agent_id": "agent_1", "issued_by": "tesoreria@example",
  "valid_until": "2027-01-01T00:00:00Z",
  "allowed_operations": ["BUY","SELL"], "allowed_asset_classes": ["EQUITY"],
  "per_order_notional": 25000, "rolling_1h_notional": 100000,
  "daily_notional": 500000, "max_open_orders": 20 }
```

Grants were created with SQL by hand: the store has carried `Save` since Phase 3 and
nothing served it, so issuing authority meant a psql prompt and a hope that the columns
were right.

**Issuing is a separate privilege from submitting**, named in `GATEWAY_GRANT_ISSUERS`
and off for everything else. P-002 says the customer retains final authority, and a
credential that could both submit orders and issue the authority to submit them would
let an agent raise its own ceiling: INV-002 would still be enforced, against a limit the
party under it can move. A workload credential (A2) never carries the privilege, even
when a named issuer's bearer token rides the same connection.

The tenant is absent from the body on purpose — it comes from the credential. A grant
decides what an agent may do, and letting the request name whose authority it is would
be the cross-tenant hole in its most direct form.

`per_order_notional`, `valid_until`, `allowed_operations` and `allowed_asset_classes`
are required. A grant with no ceiling is not a generous grant, it is an absent one:
evaluation would allow every size and the limit would exist only in the mind of whoever
wrote the request. Unknown fields are refused, because a misspelled limit would
otherwise be dropped in silence and the grant issued without it.

| Status | Meaning |
|--------|---------|
| 201 | Issued. |
| 400 | The grant would not constrain anything; `details` says which limits are missing. |
| 401 | Nothing authenticated the caller. |
| 403 | This credential may not issue authority. |
| 409 | A grant with this id already exists. |

**409 rather than an upsert.** `Save` updates on conflict, and a PUT-shaped POST would
let an issuer widen an existing grant by reissuing it under the same id — the same
escalation by a slower route. Authority is not edited in place: revoke and issue anew,
so the change is two auditable acts.

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

## Exact decimals on the wire

Money and quantities are exact decimals, not floats, from the wire to the broker request.
The envelope schema and `openapi.json` now say so; until the fourth remediation they
described plain JSON numbers and the runtime quietly accepted more than that.

```text
notional, limit_price, stop_price   scale 4   (0.0001)
quantity                            scale 8   (0.00000001)
```

Both forms are supported, deliberately:

- a **JSON number** — `1000.0001` — read as the literal it is, never through binary64;
- a **quoted decimal string** — `"1000.0001"` — because several languages cannot render
  every supported decimal exactly as a JSON number, and a caller who has the right value
  should not have to lose it to the encoder.

What is refused rather than rounded:

- more than the field's scale (`AMOUNT_PRECISION_UNSUPPORTED`). `900000000000.0002` and
  `900000000000.0003` are different amounts, and only the caller can say which they meant;
- exponent notation;
- magnitudes above about 461 trillion for money and 46 billion for quantities. The parser
  refuses a whole part above 2^62 divided by the scale so the conversion cannot overflow —
  half of what an int64 would hold, and the half nobody has an order in.
