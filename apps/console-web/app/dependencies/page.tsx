import { Note, Pill, SourceStrip, Surface, Table, Unavailable } from "@/components/Surface";
import { dependencies } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Dependencies (spec section 48.3).
 *
 * The verification levels are three separate columns. Collapsing them into a single
 * "verified %" would hide the unknowns, and the unknowns are the point: a
 * concentration measured over declarations nobody checked is a different finding from
 * one measured over attested ones (ADR-007, INV-008).
 *
 * The design specification asks for a dependency graph. `DependencyRow` carries
 * per-dependency counts and no edges between dependency nodes, and the specification says
 * plainly not to fabricate them: a graph drawn from co-occurrence would assert
 * relationships the platform has never observed, which is the exact failure this surface
 * exists to prevent.
 */
export default async function DependenciesPage() {
  const deps = await dependencies();

  // The most widely shared dependency, which is what "concentration" means here: how
  // many agents rest on one thing. Computed from the rows the endpoint returned and
  // nothing else — a summary assembled from anything the console inferred would be a
  // platform measurement in appearance only.
  const concentration = deps.available
    ? deps.rows.reduce<(typeof deps.rows)[number] | undefined>(
        (most, d) => (most === undefined || d.agents > most.agents ? d : most),
        undefined,
      )
    : undefined;

  return (
    <Surface
      current="/dependencies"
      title="Dependencies"
      summary="What the fleet declared it depends on, and how well sourced each declaration is."
    >
      <SourceStrip
        source="GET /v1/dependencies"
        availability={!deps.available ? "unavailable" : deps.rows.length === 0 ? "empty" : "live"}
        detail={
          deps.available && concentration
            ? `most shared: ${concentration.dependency_id} (${concentration.agents} agents)`
            : undefined
        }
      />

      {!deps.available ? (
        <Unavailable reason={deps.reason} />
      ) : (
        <Table
          columns={[
            "type",
            "dependency",
            "agents",
            "observations",
            "provenance",
            "last seen",
          ]}
          rows={deps.rows.map((d) => [
            d.dependency_type,
            <span key="id" className="x-mono">
              {d.dependency_id}
            </span>,
            d.agents,
            d.observations,
            // Three axes, kept apart. A single "verified %" would bury the unknowns, and
            // the unknowns are what decides whether a concentration is a finding or a
            // guess. Two badges is the compact limit the data language sets; the third
            // is shown here because all three are the measurement.
            <span key="p" className="x-pill-row">
              <Pill tone="verified">V {d.verified}</Pill>
              <Pill tone="declared">D {d.declared}</Pill>
              <Pill tone="unknown">U {d.unknown}</Pill>
            </span>,
            <span key="s" className="x-mono">
              {d.last_seen}
            </span>,
          ])}
        />
      )}

      <Note>
        An unknown declaration is counted as unknown, never as a model or feed of its
        own. Counting them would invent a large fictitious competitor and make every
        real concentration look smaller than it is.
      </Note>

      <Note>
        There is no dependency graph. The endpoint reports how many agents share each
        dependency and never which dependencies relate to each other, so any edge drawn
        here would be inferred from co-occurrence and rendered as though it had been
        observed. A graph needs a backend contract that supplies relationships.
      </Note>
    </Surface>
  );
}
