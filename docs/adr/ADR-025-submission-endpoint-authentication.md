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

## Consequences

- A bearer-authenticated caller cannot claim A2 or A3. It gets exactly what its
  transport established, which is the taxonomy working as designed.
- The credential registry is minimal: no rotation, no scoping beyond identity, and
  configuration through the environment. Real credential management is a secret
  manager (spec section 35), and this does not pretend to be one.
- An operator who wants A2 deploys SPIRE and configures a trust bundle. Nothing in
  the gateway changes.

## Prohibited reinterpretations

- The bearer path must not be extended to claim A2 "because the caller is trusted".
  A2 means a workload was attested; a shared secret attests nothing.
- The read endpoints' header-based tenant must never be reused here. A header is a
  claim, and this endpoint may not act on claims.
