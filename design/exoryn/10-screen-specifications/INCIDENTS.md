# Incidents Screen Specification

## Purpose
Reconstruct what happened and distinguish system recommendation from customer action.

## Current sources
`GET /v1/incidents` and evidence query by correlation ID. Timeline is rebuilt from evidence and is authoritative over the incident projection.

## V1 layout
Wide screens: incident queue | evidence timeline | context inspector.

Context may include cohort, shared dependencies and controls when supplied.

## Mandatory
- severity is categorical;
- recommendation labeled `Recommended only` unless evidence says control enforced;
- actual control labeled `Enforcing/Applied` with actor when present;
- human actions distinct;
- raw evidence available;
- projection disagreement defers to evidence.
