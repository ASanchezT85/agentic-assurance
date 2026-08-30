import type { ReactNode } from "react";

import { Note, Panel, Pill, SourceStrip, Surface, Table, Unavailable } from "@/components/Surface";
import { simulations } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Lab (spec section 48.5).
 *
 * This page said the simulator's records were not stored and that POST /v1/simulations
 * and GET /v1/simulations/{id} had not been built. Both were true when written and
 * neither survived the phase that built them, so the surface about reproducibility was
 * the one telling readers the least reproducible thing: a claim nobody had rechecked.
 *
 * It reads the runs now. A run is listed with its seed and its two hashes, because a
 * result nobody can rerun is an anecdote.
 */
export default async function LabPage() {
  const result = await simulations();

  return (
    <Surface
      current="/lab"
      title="Lab"
      summary="Reproducible experiments against the Digital Twin."
    >
      <SourceStrip
        source="GET /v1/simulations"
        availability={
          !result.available ? "unavailable" : result.rows.length === 0 ? "empty" : "live"
        }
        detail={
          result.available && result.rows.length > 0
            ? `${result.rows.length} run${result.rows.length === 1 ? "" : "s"}`
            : undefined
        }
      />

      {!result.available ? (
        <Unavailable reason={result.reason} />
      ) : result.rows.length === 0 ? (
        <p className="x-empty">No experiment has been run in this tenant.</p>
      ) : (
        <Table
          columns={["run", "scenario", "seed", "status", "requested by", "fingerprint"]}
          rows={result.rows.map((r) => [
            <span key="id">
              <code>{r.run_id}</code>
              <div className="x-cell-sub">{r.requested_at}</div>
            </span>,
            <span key="s">
              {r.scenario}
              {r.scenario_source_hash ? (
                <div className="x-cell-sub">
                  <code>{r.scenario_source_hash.slice(0, 12)}</code>
                </div>
              ) : null}
            </span>,
            <code key="seed">{r.seed}</code>,
            <RunStatus key="st" status={r.status} />,
            r.requested_by,
            r.result_fingerprint ? (
              <code key="f">{r.result_fingerprint.slice(0, 12)}</code>
            ) : (
              <span key="f" className="x-empty">
                no result yet
              </span>
            ),
          ])}
        />
      )}

      <Panel title="Why the hashes are shown">
        <p>
          <code>scenario_source_hash</code> is a sha256 of the scenario file&apos;s exact
          bytes, so a record says <em>which file</em> ran rather than only what it was
          called. <code>result_fingerprint</code> is the engine&apos;s own hash of the
          result. Same seed and same source hash and a different fingerprint means the
          engine changed, which is the finding a reproducibility surface exists to make
          visible.
        </p>
        <p style={{ marginBottom: 0 }}>
          Starting a run is <code>POST /v1/simulations</code> on the fleet engine. It is
          not built here: the console reads (spec section 59), and a lab that could
          launch work would make it something production depends on.
        </p>
      </Panel>

      <Note>
        There is no compare-runs view. Comparing two runs means showing where their
        results diverged, and the list carries a fingerprint rather than a result — the
        console can say two runs differ and not in what, which is the half that would be
        worth showing.
      </Note>
    </Surface>
  );
}

/**
 * A run's status, in the colours the data language assigns.
 *
 * CANCELLED is deliberately not FAILED: nothing went wrong, someone changed their mind,
 * and a failure count that included cancellations would make the engine look unreliable.
 * It reads as neutral for that reason, not as a lesser failure.
 */
function RunStatus({ status }: { status: string }): ReactNode {
  const normalized = status.toUpperCase();
  const tone =
    normalized === "COMPLETED"
      ? "success"
      : normalized === "FAILED"
        ? "danger"
        : normalized === "CANCELLED"
          ? "unknown"
          : "warning";
  return <Pill tone={tone}>{normalized}</Pill>;
}
