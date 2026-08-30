# ADR-028: Policy authority is bootstrapped once, and then extends only itself

**Status:** Accepted
**Date:** 2026-08-30
**Phase:** V0
**Relates to:** ADR-010 (signed policy activation), INV-009, P-002

## Context

Since the third remediation a policy bundle enforces only when the customer has signed an
authorization for it, verified against a key in `policy_activation_keys`. That closed the
hole where anyone who could edit a policy file could promote a SHADOW bundle to ACTIVE
without possessing a key.

It left one thing unbuilt: nothing registered those keys. The only way a tenant came to
hold an activation key was a row written by hand, or `live-setup` in development. So
taking custody of your own policy authority required database access — the same shape as
the agent-key gap the previous endpoint closed, but on the strongest object in the system.

An activation key is not an agent key with a different table. An agent key says which key
*is* an agent; an activation key says which bundle *enforces*, and a bundle says what
every agent in the tenant may not do. Whoever can add one can hand themselves the power to
lift every ceiling the customer set, without touching a single grant or agent key.

That makes the obvious design wrong. If an operator credential could register activation
keys the way it registers agent keys, the platform's own operator could add a signer to
any customer, sign an activation with it, and enforce a policy the customer never
approved — while every signature in the evidence chain verified perfectly. The signature
scheme would be intact and the authority behind it would be the platform's.

## Decision

**A tenant's first activation key is bootstrapped by a named operator credential. Every
later key is registered only by an authorization signed by a key the tenant already
holds.**

- `POST /v1/policy-activation-keys` serves both. Which one it is, is not the caller's
  choice: the number of active keys the tenant holds decides it.
- The bootstrap is gated by `GATEWAY_ACTIVATION_KEY_REGISTRARS` — a third privilege list,
  separate from `GATEWAY_GRANT_ISSUERS` and `GATEWAY_KEY_REGISTRARS`. A workload-attested
  caller (A2) never carries it: an SVID says which workload is running, not which person
  authorized it.
- Once a tenant holds one active key, that credential can no longer add any. It can only
  present the customer's signed `KeyAuthorization`, verified against a registered,
  usable key. The platform never signs what constrains it (INV-009).
- A key may not authorize its own registration. Self-signing would be the bootstrap path
  without the credential that gates the bootstrap.
- The authorization carries a nonce, and the nonce is the primary key of
  `policy_activation_key_grants`. A captured authorization presented twice is refused by
  the database on every replica rather than by whichever process remembered it.
- The key row and its grant record are written in one transaction, with the evidence
  event. A key able to decide which policy enforces cannot become usable through a commit
  that did not also record who granted it.
- An existing key is never overwritten. `ActivationStore.RegisterKey` used to upsert, so a
  registration under a taken key id silently substituted the authority that decides which
  policy enforces — and told the caller nothing. Rotation is a new key id, then a
  revocation.
- Revocation (`POST /v1/policy-activation-keys/revoke`) is **not** signed for. The case
  that matters is a key believed compromised, and requiring that key's cooperation to
  retire it would be requiring the attacker's. It is gated by the same privilege.
- The last active key cannot be revoked. A tenant with none can never authorize another
  policy change — including the rollback an incident needs — and recovering from that
  needs database access, which is the state this endpoint exists to remove.

## Consequences

- A customer can take custody of their policy authority and extend it without anyone
  touching the database, which was the last remaining gap of that kind.
- The operator's power is bounded in time rather than in scope: it exists for a tenant
  until that tenant has one key, and never again. An operator who bootstraps a key for a
  tenant that already has one is refused, so the escalation is not available even to a
  compromised operator credential.
- A tenant that loses every private key it registered is stuck: no new key can be signed
  for, and the bootstrap is spent. This is deliberate — the alternative is an operator
  path back in, which is the escalation itself — and it makes key custody a customer
  obligation. Registering a second key held separately is the documented answer, and the
  last-key rule keeps at least one alive.
- `RegisterKey` and `RevokeKey` now report whether they did anything. Callers that read
  "no error" as "done" were wrong in both directions.

## Alternatives rejected

**Reuse the agent-key registrar privilege.** One list is simpler and it merges the two
powers this platform spends most of its design separating: what an agent may do, and what
may constrain every agent. An operator granted agent onboarding would silently hold policy
authority.

**Let the operator credential register any activation key.** It makes rotation easy and
makes the whole signed-activation scheme decorative: the platform could always mint a
signer for itself.

**Require a signature to revoke.** Symmetrical, and wrong for the only case that matters.
