import { Surface, Unavailable } from "@/components/Surface";
import { evidenceFor, incidents } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Incidents (spec section 48.4).
 *
 * A timeline is reconstructed from evidence, never from the incidents table. There is
 * one now, and it is a projection: a second copy of the record can disagree with the
 * record, and when it does the evidence is right. The list below is that projection,
 * and every timeline under it is rebuilt from the chain.
 *
 * The two lines that matter are kept apart throughout: what the system recommended,
 * and what a person did. Spec section 49 ends on exactly that distinction, and a
 * screen that blurs it cannot answer the last question an incident review asks.
 */
export default async function IncidentsPage({
  searchParams,
}: {
  searchParams: Promise<{ correlation_id?: string }>;
}) {
  const params = await searchParams;
  const correlationId = params.correlation_id?.trim() ?? "";

  return (
    <Surface
      title="Incidents"
      summary="Timelines rebuilt from the append-only record. If this disagrees with anything, this is right."
    >
      <form method="get" style={{ marginBottom: "1.5rem" }}>
        <label htmlFor="correlation_id">Correlation id&nbsp;</label>
        <input
          id="correlation_id"
          name="correlation_id"
          defaultValue={correlationId}
          style={{ padding: "0.3rem 0.5rem", minWidth: "22rem" }}
        />
        <button type="submit" style={{ marginLeft: "0.5rem", padding: "0.3rem 0.8rem" }}>
          Rebuild timeline
        </button>
      </form>

      {correlationId === "" ? (
        <OpenIncidents />
      ) : (
        <Timeline correlationId={correlationId} />
      )}
    </Surface>
  );
}

async function OpenIncidents() {
  const result = await incidents();
  if (!result.available) {
    return <Unavailable reason={result.reason} />;
  }
  if (result.rows.length === 0) {
    return (
      <p>
        No incident has been opened in this tenant. Enter a correlation id above to
        rebuild a timeline from evidence anyway — the chain outlives the projection.
      </p>
    );
  }

  return (
    <table style={{ borderCollapse: "collapse", width: "100%" }}>
      <thead>
        <tr style={{ textAlign: "left" }}>
          <th>Opened</th>
          <th>Severity</th>
          <th>Why</th>
          <th>Recommended</th>
          <th>Timeline</th>
        </tr>
      </thead>
      <tbody>
        {result.rows.map((i) => (
          <tr key={i.incident_id} style={{ borderTop: "1px solid currentColor" }}>
            <td>{i.opened_at}</td>
            <td>
              {i.severity}
              <div style={{ opacity: 0.75 }}>{i.severity_rule}</div>
            </td>
            <td>{i.anomaly_rules.join(", ")}</td>
            {/* Recommended, never "applied". The platform recommends and a customer
                authorizes (INV-009); the Controls surface lists what actually binds. */}
            <td style={{ opacity: 0.75 }}>{i.recommended}</td>
            <td>
              <a href={`/incidents?correlation_id=${encodeURIComponent(i.correlation_id)}`}>
                rebuild
              </a>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

async function Timeline({ correlationId }: { correlationId: string }) {
  const chain = await evidenceFor(correlationId);
  if (!chain.available) {
    return <Unavailable reason={chain.reason} />;
  }

  const relevant = chain.rows.filter(
    (e) => e.event_name.startsWith("incident.") || e.event_name.startsWith("control."),
  );

  if (relevant.length === 0) {
    return <p>No incident evidence for that correlation id in this tenant.</p>;
  }

  const ordered = [...relevant].sort((a, b) => a.occurred_at.localeCompare(b.occurred_at));

  return (
    <>
      <h2 style={{ fontSize: "1rem" }}>Timeline</h2>
      <ol style={{ paddingLeft: "1.2rem" }}>
        {ordered.map((event) => {
          const payload = event.payload ?? {};
          const enforced = payload["enforced"] === true;
          const actor = typeof payload["actor"] === "string" ? payload["actor"] : "";
          const isControl = event.event_name.startsWith("control.");

          return (
            <li key={event.event_id} style={{ marginBottom: "0.8rem" }}>
              <div>
                <strong>{event.occurred_at}</strong> {event.event_name}
              </div>

              {isControl ? (
                <div>
                  {enforced ? (
                    <>
                      <strong>Applied.</strong> A control that took effect
                      {actor ? `, by ${actor}` : ""}.
                    </>
                  ) : (
                    <>
                      <strong>Recommended only.</strong> Nothing was enforced. Customer
                      policy authorizes enforcement, not this system.
                    </>
                  )}
                </div>
              ) : null}

              {actor && !isControl ? <div>Human action by {actor}.</div> : null}

              <pre style={{ margin: "0.3rem 0 0", fontSize: "0.75rem", whiteSpace: "pre-wrap" }}>
                {JSON.stringify(payload, null, 2)}
              </pre>
            </li>
          );
        })}
      </ol>
    </>
  );
}
