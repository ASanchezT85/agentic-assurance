# ADR-002 — Canonical intent model

**Status:** LOCKED (MASTER_BUILD_SPEC.md §6)

## Context
Inbound protocols multiply: REST today, MCP soon, SDKs and broker-specific APIs later.
If each carries its own shape into the core, policy has to be written once per
protocol and the invariants stop being enforceable in one place.

## Decision
`AgentExecutionEnvelope` is the canonical contract (§12). MCP, REST, SDKs,
broker-specific APIs and future protocols translate into this envelope before any
policy runs.

## Consequences
- Adapters own translation and nothing else.
- Policy, authority, lineage and evidence are written once against the envelope.
- A field that matters must be in the envelope, not smuggled through adapter metadata.

## Prohibited reinterpretations
- No policy may read a protocol-specific field.
- A protocol that cannot express a required envelope field is not exempt from it. The
  adapter must fail the request.
