import { Surface, Table, Unavailable } from "@/components/Surface";
import { fleetState } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Fleet (spec section 48.1).
 *
 * Coverage is displayed beside every measurement rather than behind a tooltip.
 * Section 28 forbids collapsing low-confidence data into a precise score, and a
 * directional imbalance of 0.97 over 20% coverage is not the same finding as one over
 * 95%.
 */
export default async function FleetPage() {
  const state = await fleetState();

  return (
    <Surface
      current="/fleet"
      title="Fleet"
      summary="Measurements per cohort per window, as they were computed. Nothing here is recalculated on read."
    >
      {!state.available ? (
        <Unavailable reason={state.reason} />
      ) : (
        <Table
          columns={[
            "cohort",
            "window",
            "intents",
            "agents",
            "gross",
            "net",
            "D",
            "observed coverage",
            "verified coverage",
            "unknown coverage",
          ]}
          rows={state.rows.map((m) => [
            m.cohort_predicate,
            `${m.window_start} → ${m.window_end}`,
            m.intent_count,
            m.agent_count,
            m.gross_notional.toFixed(2),
            m.net_notional.toFixed(2),
            m.directional_imbalance.toFixed(4),
            m.observed_coverage.toFixed(2),
            m.verified_coverage.toFixed(2),
            m.unknown_coverage.toFixed(2),
          ])}
        />
      )}

      <p style={{ marginTop: "2rem", opacity: 0.7 }}>
        There is no composite risk score on this page and there will not be one until a
        calibrated model and its own ADR exist (ADR-014). The Fleet Risk Vector is eight
        components, each carrying its own coverage.
      </p>
    </Surface>
  );
}
