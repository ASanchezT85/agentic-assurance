import { Note, Pill, SourceStrip, Surface, Table, Unavailable } from "@/components/Surface";
import { fleetState } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Fleet (spec section 48.1).
 *
 * Coverage is displayed beside every measurement rather than behind a tooltip.
 * Section 28 forbids collapsing low-confidence data into a precise score, and a
 * directional imbalance of 0.97 over 20% coverage is not the same finding as one over
 * 95%.
 *
 * The design specification asks for connected agents, attestation coverage, intent rate
 * and abnormal cohorts above the table. `GET /v1/fleet/state` supplies none of them, and
 * the specification's own rule for that case is to leave the slot out rather than
 * synthesize it. So the surface shows what the endpoint returns, and says what is
 * missing.
 */
export default async function FleetPage() {
  const state = await fleetState();

  return (
    <Surface
      current="/fleet"
      title="Fleet"
      summary="Measurements per cohort per window, as they were computed. Nothing here is recalculated on read."
    >
      <SourceStrip
        source="GET /v1/fleet/state"
        availability={!state.available ? "unavailable" : state.rows.length === 0 ? "empty" : "live"}
        detail={
          state.available && state.rows.length > 0
            ? `${state.rows.length} measurement${state.rows.length === 1 ? "" : "s"}`
            : undefined
        }
      />

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
            <span key="w" className="x-mono">
              {m.window_start} → {m.window_end}
            </span>,
            m.intent_count,
            m.agent_count,
            m.gross_notional.toFixed(2),
            m.net_notional.toFixed(2),
            m.directional_imbalance.toFixed(4),
            // Coverage travels with the measurement it qualifies, as a badge rather than
            // a bare number: 0.20 in a column of numbers reads as a small value, and it
            // is not a value — it is how much of the window was seen at all.
            <Pill key="o" tone={coverageTone(m.observed_coverage)}>
              {m.observed_coverage.toFixed(2)}
            </Pill>,
            <Pill key="v" tone={coverageTone(m.verified_coverage)}>
              {m.verified_coverage.toFixed(2)}
            </Pill>,
            <Pill key="u" tone="unknown">
              {m.unknown_coverage.toFixed(2)}
            </Pill>,
          ])}
        />
      )}

      <Note>
        There is no composite risk score on this page and there will not be one until a
        calibrated model and its own ADR exist (ADR-014). The Fleet Risk Vector is eight
        components, each carrying its own coverage.
      </Note>

      <Note>
        Connected agents, attestation coverage, intent rate and abnormal cohorts are not
        shown. <code>GET /v1/fleet/state</code> does not supply them, and a figure
        assembled here from what the console happens to have would be a platform
        measurement in appearance only.
      </Note>
    </Surface>
  );
}

/**
 * Coverage is not a score, and its colour says only whether the measurement beside it
 * rests on much of the window or little of it.
 *
 * Deliberately not a gradient: three bands, so an operator reads a category rather than
 * estimating a shade. Low coverage is a warning about the number next to it, not an
 * incident about the fleet.
 */
function coverageTone(coverage: number): "success" | "warning" | "unknown" {
  if (coverage >= 0.8) return "success";
  if (coverage >= 0.4) return "unknown";
  return "warning";
}
