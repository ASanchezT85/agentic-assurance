import { Surface, Unavailable } from "@/components/Surface";
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
export default function LabPage() {
  return (
    <Surface title="Lab" summary="Reproducible experiments against the Digital Twin.">
      <Runs />

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>Why the hashes are shown</h2>
      <p>
        <code>scenario_source_hash</code> is a sha256 of the scenario file&apos;s exact
        bytes, so a record says <em>which file</em> ran rather than only what it was
        called. <code>result_fingerprint</code> is the engine&apos;s own hash of the
        result. Same seed and same source hash and a different fingerprint means the
        engine changed, which is the finding a reproducibility surface exists to make
        visible.
      </p>
      <p style={{ opacity: 0.75 }}>
        Starting a run is <code>POST /v1/simulations</code> on the fleet engine. It is
        not built here: the console reads (spec section 59), and a lab that could
        launch work would make it something production depends on.
      </p>
    </Surface>
  );
}

async function Runs() {
  const result = await simulations();
  if (!result.available) {
    return <Unavailable reason={result.reason} />;
  }
  if (result.rows.length === 0) {
    return <p>No experiment has been run in this tenant.</p>;
  }

  return (
    <table style={{ borderCollapse: "collapse", width: "100%" }}>
      <thead>
        <tr style={{ textAlign: "left" }}>
          <th>Run</th>
          <th>Scenario</th>
          <th>Seed</th>
          <th>Status</th>
          <th>Requested by</th>
          <th>Fingerprint</th>
        </tr>
      </thead>
      <tbody>
        {result.rows.map((r) => (
          <tr key={r.run_id} style={{ borderTop: "1px solid currentColor" }}>
            <td>
              <code>{r.run_id}</code>
              <div style={{ opacity: 0.75 }}>{r.requested_at}</div>
            </td>
            <td>
              {r.scenario}
              {r.scenario_source_hash ? (
                <div style={{ opacity: 0.75 }}>
                  <code>{r.scenario_source_hash.slice(0, 12)}</code>
                </div>
              ) : null}
            </td>
            <td>{r.seed}</td>
            {/* CANCELLED is deliberately not FAILED: nothing went wrong, someone
                changed their mind, and a failure count that included cancellations
                would make the engine look unreliable. */}
            <td>{r.status}</td>
            <td>{r.requested_by}</td>
            <td>
              {r.result_fingerprint ? (
                <code>{r.result_fingerprint.slice(0, 12)}</code>
              ) : (
                <span style={{ opacity: 0.75 }}>no result yet</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
