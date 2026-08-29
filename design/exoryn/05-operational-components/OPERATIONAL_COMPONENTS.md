# Operational Components

## FleetRiskVector
Displays components separately; no total score. Every component shows value + coverage/confidence + window. Supports D, B, C_m, C_s, C_f, P, A, Q from the V0 model.

## CoverageStack
Shows verified / declared / unknown / observed proportions with printed percentages. Unknown is a first-class segment.

## CohortCard
Predicate, agents, intents, time window, abnormality evidence and provenance coverage. Never labels a cohort “malicious” without such a backend determination.

## IntentRow / IntentSummary
Envelope ID, time, side, canonical instrument ID, last recorded event, exact recorded outcome and chain link. BUY/SELL is neutral text, not green/red.

## EvidenceTimeline
Chronological append-only chain. Shows event name, timestamp, producer, sequence, causation, correction/reconciliation markers and raw payload drawer.

## DependencyGraph
Nodes represent declared/verified model, strategy, feed or other dependency identities already provided by backend. Encodes concentration and provenance separately. Unknown declarations do not become a fictitious dependency node.

## IncidentCard
Severity, rules, opened time, cohort/dependency summary and recommendation. It does not claim a recommendation was applied.

## IncidentTimeline
Evidence-derived chronology with separate lanes/labels for detection, recommendation, control, and human action.

## ControlState
Action, exact scope, authorized-by, applied/expires, in-force/revoked/expired. “Whole tenant” is displayed only when API scope truly is tenant-wide.

## SimulationRun
Run ID, scenario, seed, source hash, status, requested by, result fingerprint. CANCELLED is not FAILED.

## ReproducibilityPair
Compares two runs and highlights seed/source hash/fingerprint agreement without inventing a pass result the backend does not provide.

## SystemHealth / SourceStatus
Shows reachable/authenticated/fresh status of gateway and fleet-engine data sources. It must never imply production enforcement health solely from the Console being available.
