# Threat model

Scope: the assurance layer between an AI-generated financial intent and a broker.
Reasoning inside the agent is outside the boundary (ADR-001, ADR-006).

This document does not claim regulatory compliance certification, and does not claim
market manipulation detection equivalent to exchange surveillance (§2.3).

## Security invariants

Fifteen invariants, stable identifiers, each owned by the phase that first makes it
violable (ADR-024). Phase 15 is the completeness and regression gate, not the first
time any of them is checked.

| ID | Invariant | Owning phase | Test |
|---|---|---|---|
| INV-001 | An unauthenticated workload can never create an executable order. | 2 | `tests/security/INV-001_unauthenticated_workload_test.go` |
| INV-002 | An agent can never exercise more authority than its active grant. | 3 | `tests/security/INV-002_authority_ceiling_test.go` |
| INV-003 | No LLM output can bypass deterministic policy. | 4 | `tests/security/INV-003_policy_determinism_test.go` |
| INV-004 | No ambiguous broker timeout may trigger blind duplicate execution. | 5 | `tests/security/INV-004_no_blind_retry_test.go` |
| INV-005 | Loss of the intelligence cloud cannot disable local hard limits. | 4 | `tests/security/INV-005_local_enforcement_test.go` |
| INV-006 | Historical evidence cannot be silently mutated. | 6 | `tests/security/INV-006_append_only_test.go` |
| INV-007 | Tenant A cannot observe Tenant B data. | 3 | `tests/security/INV-007_tenant_isolation_test.go` |
| INV-008 | Unknown provenance can never be represented as verified provenance. | 1 | `tests/security/INV-008_provenance_no_upgrade_test.go` |
| INV-009 | Fleet intelligence may recommend; customer policy authorizes enforcement. | 13 | `tests/security/INV-009_shadow_authority_test.go` |
| INV-010 | A new policy cannot reach production without versioning and validation. | 4 | `tests/security/INV-010_policy_lifecycle_test.go` |
| INV-011 | Redis loss cannot destroy authoritative financial-control state. | 5 | `tests/security/INV-011_redis_not_truth_test.go` |
| INV-012 | A broker adapter failure cannot corrupt the canonical core domain model. | 5 | `tests/security/INV-012_adapter_containment_test.go` |
| INV-013 | Audit logs and application logs are not interchangeable. | 6 | `tests/security/INV-013_log_separation_test.go` |
| INV-014 | Model identity must never be inferred from workload identity without evidence. | 2 | `tests/security/INV-014_no_identity_inference_test.go` |
| INV-015 | An invalid instrument normalization result cannot proceed to executable policy. | 1 | `tests/security/INV-015_instrument_normalization_test.go` |

Phase 1 delivered INV-008 and INV-015; Phase 2 added INV-001 and INV-014; Phase 3
added INV-002 and INV-007; Phase 4 added INV-003, INV-005 and INV-010; Phase 5 added
INV-004, INV-011 and INV-012; Phase 6 added INV-006 and INV-013; Phase 13 added
INV-009. **All fifteen now have tests.**

Phase 15 delivered that gate.

`TestEveryInvariantHasATestAndEveryTestHasAnInvariant` checks both directions: a test
file with no entry here is an invariant nobody wrote down, and an entry with no file is
one nobody checks. The second is the failure that hides for a year.

`TestEveryInvariantFileContainsAssertions` checks that the files do something. A file
that exists and asserts nothing would satisfy the completeness check and prove
nothing, which is the exact shape of a gate that has stopped working.

Under concurrency: enforcement stays correct across 6,400 interleaved compliant and
oversized orders, tenant isolation holds across four tenants evaluating each other's
grants concurrently, and policy returns the same answer in 12,800 concurrent
evaluations as it does alone.

Under chaos: `tests/chaos` stops ClickHouse, NATS, Redis, the identity issuer and
PostgreSQL, and checks the section 55 principle in both directions each time. An
oversized order is denied and a compliant one still passes, because an outage that
becomes a blanket denial is an outage of its own.

INV-006 is a database privilege before it is a test. The application role holds
`SELECT` and `INSERT` on `evidence_events` and nothing else, and a trigger rejects
UPDATE and DELETE as well, so a future migration that grants the privilege by accident
still cannot rewrite history.

INV-004 counts submissions that reached the venue rather than orders that exist. A
venue that deduplicates client order ids would hide the bug while the platform kept
committing it, and what the invariant forbids is sending the duplicate at all.

Three of these are enforced structurally rather than behaviourally. INV-003 and
INV-005 parse the enforcement packages and fail on a forbidden import, and INV-003
also fails if the evaluator ever reaches the YAML parser. A test that unplugs a
dependency proves the code copes today; a test that shows the wire does not exist
proves it cannot stop coping.

INV-007 is proven in two halves. The in-process half is an ordinary unit test; the
database half is build-tagged `integration`, because row level security cannot be
proven without a database enforcing it. That file opens by asserting the test's own
connection is not exempt from RLS: PostgreSQL exempts superusers entirely, and
`ENABLE` without `FORCE` exempts the table owner, so an isolation suite run under
either passes while the database isolates nothing. Writing an assertion against absent behavior produces a green test that
proves nothing, so `tests/security/` grows one file at a time rather than being
stubbed out up front.

## Threats and mandatory defenses

| Threat | Mandatory defense | Where it lands |
|---|---|---|
| Stolen credential | Workload identity, revocation, short-lived credentials | Phase 2, 3 |
| Forged agent identity | SVID validation | Phase 2 |
| Replay attack | Nonce / idempotency | Phase 5 |
| Retry storm | Deterministic duplicate handling plus reconciliation | Phase 5 |
| Prompt injection | Authority boundary plus policy boundary | Phase 3, 4 |
| Malicious agent | Hard execution envelope | Phase 4 |
| Compromised feed | Dependency concentration plus provenance | Phase 8, 9 |
| Model regression | Cohort comparison plus version metadata | Phase 9 |
| Strategy cloning | Strategy concentration | Phase 9 |
| Model concentration | Dependency telemetry | Phase 8 |
| Policy misconfiguration | Staged deployment plus simulation | Phase 4, 12 |
| Cloud outage | Local enforcement | Phase 4 |
| Simulator error | Deterministic replay plus versioned datasets | Phase 11 |
| Telemetry poisoning | Signed provenance where available | Phase 8 |
| Cross-tenant leakage | Row level security plus tenant-scoped credentials | Phase 3 |
| Secret leakage | KMS / secret-manager discipline | Phase 5 |
| Human operator error | Human-action audit | Phase 10 |
| Fragmented order evasion | Parent-intent reconstruction | Phase 7 |
| Cross-agent exposure accumulation | Principal-level aggregation | Phase 7 |

## Trust boundaries

1. **Agent to gateway.** The agent is untrusted. Its declared model, strategy and feed
   are DECLARED at best (ADR-006, ADR-007). Its workload may be attested to A2.
2. **Gateway to broker.** The broker is trusted for execution facts and untrusted for
   availability. Ambiguity resolves by reconciliation, never by retry (INV-004).
3. **Enforcement plane to intelligence plane.** One-directional. The intelligence
   plane may recommend and may not enforce (INV-009). It can be entirely absent.
4. **Tenant to tenant.** No path. Cross-tenant analytics are prohibited in the normal
   application path, and any future aggregate learning needs its own privacy ADR
   (§34).
5. **Human operator to system.** Trusted to act, never trusted silently. Every policy
   change, grant, revocation, throttle, halt, resume, threshold change, acknowledgment,
   closure and emergency override is evidence (§36).

## Secrets

Broker secrets are never stored in plaintext, never logged, never returned through an
API, never embedded in evidence, and never placed in telemetry payloads (§35). The
`.env.example` in this repository contains development-only, non-secret values and is
the only env file that is ever committed.

## What an attacker still gets

Stated plainly, because a threat model that lists only wins is marketing.

- An attacker who compromises an agent workload **within** its authority grant can
  transact within that grant. The grant is the ceiling, not the intent check.
- Model provenance is DECLARED for A0 through A2. An agent can lie about which model
  it used, and V0 detects that only as a statistical anomaly, never as proof.
- Correlation is not causation. Feedback coupling (§27) is informational in V0 and
  must not be presented as proof that our observed flow moved a price.
