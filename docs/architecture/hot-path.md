# Hot path

The hot path is the synchronous journey from an inbound request to an authorization
decision. Everything on it is deterministic. Everything else is asynchronous.

## Sequence

```text
 1. adapter receives the request (REST / SDK / MCP)
 2. adapter normalizes into AgentExecutionEnvelope        ADR-002
 3. envelope validation                                    invalid -> DENY
 4. identity verification + workload attestation           invalid -> DENY
    - X509-SVID chain, expiry and URI SAN checked
    - attestation level resolved from evidence, not from the envelope claim
    - a claim above the established level is refused (INV-001)
 5. idempotency lookup: Redis cache, PostgreSQL truth      ADR-015
       resolved record  -> return the deterministic prior outcome, stop
       pending record   -> a previous attempt died mid-flight: reconcile, never resubmit
       store unreadable -> DENY; nothing is sent unrecorded
 6. authority grant evaluation                             §14.3 -> DENY
    - tenant checked first: a cross-tenant grant is INV-007, not a mismatch
    - lifecycle before limits, so a revoked grant never reports "limit exceeded"
    - a limit that cannot be read denies (spec §17)
 7. parent-intent state update (deterministic clustering)  §20
 8. hard policy evaluation against the active bundle       §15.2
    - compiled rules only; the evaluator never reaches a YAML parser
    - most restrictive action wins, so rule order does not change outcomes
    - the decision records bundle id, version and content hash
    - no bundle loaded -> DENY
 9. decision + idempotency record written to PostgreSQL    one transaction
10. control action returned to the caller
11. broker submission through BrokerAdapter, if ALLOW      ADR-012
--- synchronous boundary ends here ---
12. telemetry and evidence emitted to NATS JetStream       asynchronous
```

Steps 1 through 11 are the hot path. Step 12 and everything downstream of it are not.

## What is forbidden on the hot path

| Forbidden | Authority |
|---|---|
| Any LLM or non-deterministic model | ADR-004 |
| ClickHouse | §59 |
| fleet-engine | §29, §17 |
| Temporal | §9.8 |
| The web console | §59 |
| The intelligence cloud | ADR-003, INV-005 |
| Dynamic YAML policy interpretation | §15.2 |
| A blind retry after an ambiguous broker timeout | §17, INV-004 |

## Dependencies that can be down

| Down | Hot path behavior |
|---|---|
| fleet-engine | Unaffected. Enforcement continues. |
| ClickHouse | Unaffected. Analytics degrade. |
| NATS | Unaffected. Telemetry buffers locally within bounds (ADR-021). |
| Redis | Unaffected but slower. Idempotency reads fall through to PostgreSQL (ADR-015). |
| console-web | Unaffected. |
| simulation-engine | Unaffected. |
| **PostgreSQL** | **Executable intents are DENIED.** It holds authority, policy bundle and idempotency truth (ADR-021). |

## Latency budget

Targets from §50.1, measured excluding external network and broker latency: p50 under
2 ms, p95 under 5 ms, p99 under 10 ms.

The PostgreSQL write at step 9 is the largest single item in that budget. It is
measured in Phase 5. If it does not fit, the remedy is batching or a local
write-ahead log. It is never moving idempotency truth into Redis (ADR-015).

## Ambiguous broker outcome

```text
submit -> timeout -> state = UNKNOWN -> reconcile with broker
                                          resolved?  yes -> return result
                                                     no  -> stay UNKNOWN, operator flow
```

No blind retry, ever (§54, INV-004).

## Parent intent reconstruction (step 7)

Deterministic clustering, never inference (spec section 20.2). Intents are grouped by
tenant, principal, account, instrument and side, then split wherever consecutive
orders are further apart than the window.

The grouping key deliberately **excludes the agent**. Cross-agent accumulation under
one principal is the case spec section 21 exists for, and keying on agent would make
it invisible by construction.

Two numbers are reported rather than one. `GrossNotional` is the total committed;
`NetNotional` carries the direction, because a cluster of buys and a cluster of sells
that net to zero are very different situations (spec section 23).

`IndeterminateChildren` counts orders whose notional cannot be established without a
market price. They are counted rather than dropped: a fragmented intent hidden behind
market orders is exactly the evasion this engine exists to see, and a gross total that
silently omitted them would understate exposure. `ExposureComplete()` tells a caller
whether it is looking at the whole picture.

Confidence is the fraction of nine enumerated signals that held across the cluster.
It is a coverage ratio with a stated denominator, not a risk score with chosen
weights (ADR-014), and the signals themselves are reported so a reader can disagree
with the conclusion by looking at the same facts.

The window (60s) and minimum cluster size (2) are documented defaults, not calibrated
thresholds. Their provenance is "nobody has measured yet", and spec section 60
forbids treating that as a threshold anyone enforces on.
