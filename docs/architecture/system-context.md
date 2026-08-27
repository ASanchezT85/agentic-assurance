# System context

## What this platform is

Infrastructure for financial institutions to attest, authorize, monitor, stress-test
and contain AI-generated order flow before it becomes operational or market risk.

## What it is not

It is not a trading bot, a recommendation engine, a robo-advisor, a brokerage, a
stock picker, a generic MCP server, or a generic AI-governance dashboard (ADR-001,
spec §0).

## Boundary

The product boundary begins where a structured financial intent already exists.

```text
LLM / agent reasoning
        |
        |  OUTSIDE the product boundary
        v
AgentExecutionEnvelope
        |
        |  INSIDE the product boundary
        v
Identity -> Authority -> Policy -> Lineage -> Fleet intelligence -> Evidence
        |
        v
Broker / OMS / EMS
```

Reasoning happens outside. We never see it, and we never claim to (ADR-006).

## Actors

| Actor | Relationship |
|---|---|
| AI agent | Submits intents through REST, an SDK, or the MCP adapter (ADR-002). |
| Principal / account holder | The party whose authority an agent exercises. |
| Financial institution (customer) | Owns the enforcement plane and the final policy (ADR-003). |
| Human operator | Acts on the console. Audited exactly as agents are (§36). |
| Broker / OMS / EMS | Downstream execution venue, reached only through an adapter (ADR-012). |
| Security reviewer / auditor | Reads the append-only evidence chain (ADR-023). |

## Questions the system answers

Who generated the action. Whether that actor had authority. Whether the action
violates deterministic policy. Whether it is part of a larger economic intent. Whether
similar actions are emerging across a fleet. Whether dependencies are concentrated.
Whether behavior deviates from baseline. What the customer-controlled system should
do.

## Questions the system refuses to answer

What to buy, what to sell, when to enter, when to exit, how to maximize returns.
