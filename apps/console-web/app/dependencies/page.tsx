import { Surface, Table, Unavailable } from "@/components/Surface";
import { dependencies } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Dependencies (spec section 48.3).
 *
 * The verification levels are three separate columns. Collapsing them into a single
 * "verified %" would hide the unknowns, and the unknowns are the point: a
 * concentration measured over declarations nobody checked is a different finding from
 * one measured over attested ones (ADR-007, INV-008).
 */
export default async function DependenciesPage() {
  const deps = await dependencies();

  return (
    <Surface
      current="/dependencies"
      title="Dependencies"
      summary="What the fleet declared it depends on, and how well sourced each declaration is."
    >
      {!deps.available ? (
        <Unavailable reason={deps.reason} />
      ) : (
        <Table
          columns={["type", "dependency", "agents", "observations", "verified", "declared", "unknown", "last seen"]}
          rows={deps.rows.map((d) => [
            d.dependency_type,
            d.dependency_id,
            d.agents,
            d.observations,
            d.verified,
            d.declared,
            d.unknown,
            d.last_seen,
          ])}
        />
      )}

      <p style={{ marginTop: "2rem", opacity: 0.7 }}>
        An unknown declaration is counted as unknown, never as a model or feed of its
        own. Counting them would invent a large fictitious competitor and make every
        real concentration look smaller than it is.
      </p>
    </Surface>
  );
}
