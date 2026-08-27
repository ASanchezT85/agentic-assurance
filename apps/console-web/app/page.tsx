/**
 * Phase 0 scaffold. Per ADR-017 this page exists only to prove the TypeScript
 * toolchain compiles under strict mode in CI.
 *
 * It performs no network calls, renders no financial data, and has no auth.
 * The six console surfaces of MASTER_BUILD_SPEC.md §48 arrive in Phase 14.
 */
export default function Home() {
  return (
    <main style={{ maxWidth: "42rem" }}>
      <h1 style={{ fontSize: "1.25rem" }}>Agentic Order-Flow Assurance</h1>
      <p>Phase 0 — repository and contracts foundation.</p>
      <p>
        This console is a build target, not a user interface. See ADR-017.
        Operational surfaces are delivered in Phase 14.
      </p>
      <p>
        <strong>Real-money execution is not supported.</strong> This platform does not
        generate investment recommendations.
      </p>
      <p>Specification: MASTER_BUILD_SPEC.md at the repository root.</p>
    </main>
  );
}
