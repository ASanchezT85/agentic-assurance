# ADR-016 — simulation-engine is a Python deployable rooted at `simulator/`

**Status:** ACCEPTED (resolves an omission in MASTER_BUILD_SPEC.md v0.1)

## Context

ADR-011 locks four V0 deployables: assurance-gateway, fleet-engine, simulation-engine,
console-web. The repository layout in §8 provides an entry point for only three of
them: `cmd/assurance-gateway`, `cmd/fleet-engine`, `apps/console-web`. `simulator/`
contains modules with no declared process entry point, so simulation-engine is a
counted deployable that nothing in the tree builds.

## Decision

1. simulation-engine is a Python process, not a Go binary. Its entry point is
   `simulator/engine/__main__.py`, run as `python -m simulator.engine`.
2. Its container image is built from `infra/docker/simulation-engine.Dockerfile`.
3. No `cmd/simulation-engine/` directory is created. `cmd/` is the Go module's
   command directory; putting a Python deployable there would imply a Go binary.
4. §8's repository layout is therefore unchanged. The omission was a missing
   statement, not a missing directory.

## Consequences

- Phase 11 (Digital Twin) delivers the entry point. Until then `simulator/engine/`
  holds package scaffolding and the boot test only.
- Deployment docs must describe four images with two runtimes (Go, Python) plus the
  Next.js console, rather than assuming a single-language build.

## Prohibited reinterpretations

- simulation-engine MUST NOT be merged into fleet-engine to reduce the count to three.
  ADR-011 fixes four; the simulator's failure modes must not reach the fleet engine.
- The simulator MUST NOT become required for production execution (§17: "Simulator
  unavailable -> Production execution unaffected").
