# ADR-025: The submission endpoint authenticates; bearer credentials reach A1

**Status:** Accepted
**Date:** 2026-08-28
**Phase:** submission path

## Context

The read-only evidence endpoints take the tenant from a header, and the handler says
plainly that this is not authentication. That is defensible for a read inside the
customer's own network.

It is not defensible for `POST /v1/intents`. INV-001 says an unauthenticated workload
can never produce an executable order, and this endpoint produces executable orders.

Spec section 8 makes SPIFFE/SPIRE the identity substrate, which implies mutual TLS.
But requiring mTLS and nothing else would leave the endpoint unusable for any caller
without a SPIRE deployment, including every early integration, and an enforcement
plane nobody can call enforces nothing.

## Decision

The submission endpoint accepts two forms of identity, and no others.

1. **An X509-SVID from the transport** reaches A2. This is the intended path.
2. **A bearer credential matched against a configured registry** reaches A1: we know
   which registered caller this is, and nothing attests the workload it runs in.

Anything else is A0, and `identity.RequireExecutable` refuses it. The envelope's
claimed attestation level is checked against what was established, never the reverse.

The comparison is constant-time across every configured credential rather than a map
lookup, because a map lookup leaks through timing which prefix was right. Credentials
shorter than 32 characters are refused at startup: this is the only thing standing
between an unauthenticated caller and an executable order.

## Amendment: a credential is issued to a tenant

The decision above authenticated the *caller* and left the *tenant* to come from the
request. That was a hole, and it was exploitable.

Every tenant-scoped lookup after the identity check takes `env.TenantID`: the authority
grant, the policy bundle, the idempotency record, and the `app.tenant_id` that row
level security itself keys on. A caller authenticated as `svc_a` could submit an
envelope naming `tenant_b` and, with a grant id from that tenant, have the order
evaluated against `tenant_b`'s grant and `tenant_b`'s policy and placed at the venue.
Demonstrated with a test that placed such an order, and against the running binary.

The database half of INV-007 was enforced from the start — RLS, FORCE, a non-superuser
role, all with a test — and it was doing its job the whole time. It isolated correctly
to whichever tenant the platform gave it, and nothing had ever established which tenant
that should be. A guard on one half of a boundary reads as a guard on the boundary.

**A credential is now issued to a tenant**, written `identity@tenant=token`. That is
where a caller's tenant comes from, and a request naming a different one is refused at
the identity stage with `TENANT_NOT_AUTHENTICATED`. The refusal does not say which
tenant the caller belongs to: an error that printed both would be a probe with feedback
(spec section 45).

An identity with no established tenant is refused rather than trusted.

For a workload certificate that tenant comes from a **workload registry**, written
`spiffe://domain/path=tenant`. A mapping rather than a convention: the SPIFFE IDs SPIRE
issues here look like `spiffe://acme.example/ns/agents/sa/momentum`, a namespace and a
service account, and nothing in that names a tenant. Deriving one would mean inventing
a convention and then assigning customers by it silently — the same mistake as reading
the tenant off the request, with the path as the thing whoever writes it controls.

Three rules make the registry safe to configure:

- A prefix entry ends in `/`, and the trailing slash is required rather than implied.
  `spiffe://td/ns/prod` as a prefix would also match `spiffe://td/ns/production`: a
  different namespace, possibly a different customer, and a bug that looks like nothing
  until someone registers the longer name.
- Exact beats prefix and a longer prefix beats a shorter one. Anything ambiguous — the
  same path twice — is refused at startup, because otherwise map iteration order would
  decide which customer an order belonged to, at the moment it was placed.
- A whole trust domain is refused. It is not a tenant, and a catch-all assigns every
  workload SPIRE ever issues in that domain to one customer, including the ones added
  later by someone who never read the configuration.

A workload with no entry establishes no tenant and the request is refused naming it, so
an operator can tell a missing registry entry from an attack.

The same rule now covers the simulation API, which had the same shape with a header
instead of a body field. A run is stored, retrieved and cancelled by tenant, so a
header let anyone read another customer's simulation results and cancel their runs.

## Consequences

- A bearer-authenticated caller cannot claim A2 or A3. It gets exactly what its
  transport established, which is the taxonomy working as designed.
- The credential registry is minimal: no rotation, no scoping beyond identity and
  tenant, and configuration through the environment. Real credential management is a secret
  manager (spec section 35), and this does not pretend to be one.
- An operator who wants A2 deploys SPIRE, configures a trust bundle, and maps its
  workloads to tenants. Nothing in the gateway changes.
- A2 is reachable and tested end to end: an order placed over mutual TLS by a
  SPIRE-issued certificate, with no API credential in the configuration at all
  (`tests/integration/mtls_submission_test.go`). Before the registry it was buildable
  and unusable — the verifier accepted the certificate and the tenant check then
  refused it, correctly, forever.

## Prohibited reinterpretations

- The bearer path must not be extended to claim A2 "because the caller is trusted".
  A2 means a workload was attested; a shared secret attests nothing.
- The read endpoints' header-based tenant must never be reused here. A header is a
  claim, and this endpoint may not act on claims.

- A caller that legitimately acts for several tenants needs several credentials. That
  is the intended shape: one credential, one tenant, and no request field that can
  change which.
