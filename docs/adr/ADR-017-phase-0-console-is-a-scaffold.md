# ADR-017 — The Phase 0 console is a build target, not a UI

**Status:** ACCEPTED (resolves a contradiction in MASTER_BUILD_SPEC.md v0.1)

## Context

§57 Phase 0 states "Do not implement UI". The Phase 0 handoff requires a Next.js
application under `apps/console-web` with a minimal page, and makes "Next.js builds"
an acceptance criterion. Both can be satisfied, but the spec never writes down where
scaffold ends and UI begins. That gap is exactly where a dashboard gets built early.

## Decision

1. In Phase 0, `apps/console-web` exists solely to prove the TypeScript toolchain
   compiles under strict mode in CI.
2. It contains exactly one route, `/`, rendering the project name, the current phase,
   and a link to `MASTER_BUILD_SPEC.md`. Nothing else.
3. It performs no network calls, holds no API client, renders no financial data, and
   has no authentication.
4. This is enforced mechanically, not by intention: the repository structure check
   fails if any file under `apps/console-web/app` contains `fetch(`, `axios`, or a
   websocket constructor before Phase 14 begins.
5. The six console surfaces of §48 are delivered in Phase 14 and nowhere earlier.

## Consequences

- CI proves the toolchain without granting permission to build screens.
- The guard is a grep. It is deliberately crude; when Phase 14 starts, the guard is
  removed in the same commit that adds the first real surface.

## Prohibited reinterpretations

- "The build needed some data to be realistic" is not a reason to add a fetch.
- No chart, table, or metric tile may appear in `apps/console-web` before Phase 14.
