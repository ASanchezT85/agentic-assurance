import { SURFACES } from "@/components/Surface";

/**
 * The console home.
 *
 * Phase 14 opened the six surfaces of MASTER_BUILD_SPEC.md section 48, which is when
 * ADR-017 said the scaffold guard comes off. There are six, and there will be six:
 * section 48 ends by saying not to add dashboards without a defined acceptance
 * requirement.
 */
export default function Home() {
  return (
    <main style={{ maxWidth: "42rem" }}>
      <h1 style={{ fontSize: "1.25rem" }}>Agentic Order-Flow Assurance</h1>
      <p>Phase 14 — the six console surfaces.</p>

      <nav style={{ display: "flex", gap: "1rem", flexWrap: "wrap", margin: "1.5rem 0" }}>
        {SURFACES.map((s) => (
          <a key={s.href} href={s.href}>
            {s.label}
          </a>
        ))}
      </nav>

      <p>
        This console reads. It has no write path, and production is unaffected when it
        is down (spec section 17, section 59).
      </p>
      <p>
        Where a surface has no endpoint behind it yet, it says so and names what is
        missing. It does not render placeholder rows: an empty result and an absent
        source are different facts, and only one of them is about the fleet.
      </p>
      <p>
        <strong>Real-money execution is not supported.</strong> This platform does not
        generate investment recommendations.
      </p>
      <p>Specification: MASTER_BUILD_SPEC.md at the repository root.</p>
    </main>
  );
}
