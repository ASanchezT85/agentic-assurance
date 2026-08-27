# ADR-022 — No LLM in the assurance decision path, synchronous or not

**Status:** ACCEPTED (closes an ambiguity in MASTER_BUILD_SPEC.md §29)

## Context

ADR-004 bans LLMs from the critical synchronous authorization path. §29 says the fleet
engine "MUST NOT call LLMs synchronously". The word *synchronously* leaves an async
LLM contribution to cohort membership, baselines, or a control recommendation
technically permitted. An asynchronously produced number is exactly as unexplainable
as a synchronously produced one, which collides with P-008 (explain before scoring)
and ADR-014 (no arbitrary composite score).

## Decision

1. No LLM output may enter the computation of: the Fleet Risk Vector, cohort
   membership, baselines, anomaly events, incident candidate generation, or any
   control recommendation. This holds regardless of call timing.
2. Every cohort remains explainable by explicit predicates (§30).
3. LLMs are permitted for exactly one purpose in V0: human-readable prose summaries of
   an already-computed incident, generated on demand for a human reader.
4. Such a summary is labeled as generated, is never stored as evidence (§31), and is
   never an input to any subsequent computation.

## Consequences

- Fleet intelligence is entirely deterministic and reproducible in V0, which is what
  makes §49 incident timelines replayable from evidence.
- Any future ML or LLM contribution to scoring requires a new ADR carrying empirical
  calibration, per ADR-014.

## Prohibited reinterpretations

- "It only ranks the findings, it does not decide" is a decision-path contribution.
- "It runs in a nightly batch" does not exempt it.
- A generated summary MUST NOT be written into the append-only evidence store.
