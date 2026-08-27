# Schema packages

These packages hold the versioned contracts of the platform. Phase 0 establishes the
location, the versioning policy and the compatibility harness. Phase 1 and later fill
in the business semantics.

| Package | Contract | First implemented |
|---|---|---|
| `envelope-schema` | `AgentExecutionEnvelope` (spec §12) | Phase 1 |
| `event-schema` | Internal event catalog (spec §32) | Phase 6 |
| `policy-schema` | Policy authoring format (spec §15) | Phase 4 |
| `telemetry-sdk` | Client emission helpers | Phase 8 |

## Naming conventions

Schema files are named `<contract>.v<major>.<minor>.json` and live in the owning
package's `schemas/` directory. Example: `envelope-schema/schemas/agent-execution-envelope.v0.1.json`.

- `<contract>` is kebab-case and matches the canonical object name in the spec.
- The version in the filename, the version segment of `$id`, and the `const` of the
  document's `schema_version` property MUST be identical. The compatibility harness
  fails the build otherwise.
- Event schemas additionally carry the event name from spec §32, whose own `.v1`
  suffix is part of the event name, not the schema version. `agent.intent.received.v1`
  at schema version `0.1` is the file
  `event-schema/schemas/agent.intent.received.v1.v0.1.json`.

## Versioning policy

1. **Every schema is versioned from birth.** There is no unversioned schema.
2. **Additive changes bump the minor version.** Adding an optional property, widening
   an enum, or relaxing a constraint is a minor bump.
3. **Breaking changes bump the major version.** Removing a property, making an
   optional property required, narrowing a type or an enum, or renaming anything is a
   major bump. A major bump never edits the prior file; it adds a new one beside it.
4. **Published versions are immutable.** Once a schema file is merged and a producer
   has emitted against it, that file is frozen. Corrections are new versions. This
   mirrors ADR-009 for evidence: history is not rewritten.
5. **Consumers must tolerate unknown properties.** Producers may be ahead of
   consumers, and ADR-008 makes redelivery normal.
6. **Every schema appears in `schema-registry.json`** with its status and the phase
   that owns it. The registry and the filesystem must agree exactly.

## Compatibility harness

`packages/schema_compat_test.go` enforces items 1, 3, 5 and 6 mechanically:

- every file on disk is registered, and every registered file exists;
- filename version, `$id` version and `schema_version` const all agree;
- for any contract with more than one version, a later **minor** version may not drop
  or newly require a property relative to its predecessor;
- no schema sets `additionalProperties: false` at the document root.

Run it with `go test ./packages/...`.
