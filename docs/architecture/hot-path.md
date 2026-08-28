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

## What the fleet engine computes, and what it refuses to

The Fleet Risk Vector is eight components (spec section 22), and it is deliberately
not one number.

| | Component | Measured when |
|---|---|---|
| D | directional imbalance | any intent has a determinable notional |
| B | temporal burst | a baseline for the context has 30+ observations and non-zero spread |
| Cm | model concentration | at least one model family is declared |
| Cs | strategy concentration | at least one strategy is declared |
| Cf | feed concentration | at least one market-data dependency is declared |
| P | market participation | a market data source is configured and has volume (ADR-019) |
| A | abnormal consensus | D is known and a baseline exists |
| Q | quality | always |

Each carries its own coverage and its own sentence saying how it was produced. A
number without those cannot be argued with, and a fleet-risk figure nobody can argue
with is one nobody should act on.

**Unmeasured is not zero.** `Known: false` is a distinct state from `Value: 0`,
because zero directional imbalance means balanced flow and unmeasured means nobody
looked.

**Agreement is not abnormality.** A shares the baseline B depends on, and without one
it reports UNKNOWN rather than scoring unanimous buying as abnormal. Broad agreement
during a real event is ordinary, which is the property scenario S12 exists to prove.

**No composite.** ADR-014 requires empirical calibration and its own ADR before any
single number claims to summarise these, and a weighted average with hand-picked
weights is precisely the HRI that decision forbids.

The baselines use median and MAD rather than mean and standard deviation for a
concrete reason: one 50,000-intent burst in an hour of quiet moves a mean enough that
the *next* burst looks normal. A median does not move.

The 30-observation minimum and the coarse US-centric session labels are documented
defaults whose provenance is convention. They must be replaced by real venue
calendars and measured sample requirements before anyone enforces on a burst score.

## What S12 changed

Scenario S12 exists to punish over-detection, and it did its job.

The directional-imbalance rule originally fired on any cohort whose flow was more than
90% one-sided. That is correct on a coordinated sell-off and wrong on an earnings-day
rally, where sixty agents agree because something happened. A fleet engine that flags
every legitimate event is one whose alerts get ignored, which is worse than not having
them.

The rule now consults abnormal consensus first. When `A` is measurable and low, the
activity level already explains the agreement and no anomaly is reported. When there
is no baseline, the anomaly is reported and says so: *"there is no baseline for this
context, so whether that is abnormal here cannot be said."* Suppressing it silently
would be the opposite failure.

The burst still fires either way, and should: activity really did spike, and saying so
is useful. What the system must not do is treat agreement as the finding.

Measured across twenty windows of ordinary two-way activity at a range of intensities:
**0 false interventions**. The number is logged by the test rather than asserted at a
point, because the useful thing is the trend, and the bound only stops it silently
becoming useless.
