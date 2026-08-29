# Dependencies Screen Specification

## Purpose
Reveal concentration and shared dependencies without inventing causality.

## Current source
`GET /v1/dependencies`.

## V1 hierarchy
- concentration summary;
- dependency graph (only relationships actually provided by backend);
- provenance coverage;
- exact dependency table.

## Mandatory
VERIFIED, DECLARED and UNKNOWN remain separate. Unknown declarations are not grouped into a fake dependency.

## API gap
Current `DependencyRow` exposes per-dependency counts but not explicit edges between dependency nodes. The first implementation may render concentration nodes/list, but must not fabricate graph edges. A true relationship graph requires an API contract that supplies relationships.
