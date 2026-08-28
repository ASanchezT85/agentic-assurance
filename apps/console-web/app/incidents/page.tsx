import { Surface, Unavailable } from "@/components/Surface";
import { evidenceFor } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Incidents (spec section 48.4).
 *
 * An incident is reconstructed from evidence, not read from an incident table. There
 * is no incident table on purpose: a second copy of the record can disagree with the
 * record, and when it does the evidence is right.
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
        <p>
          Enter a correlation id. There is no incident list because incidents are not
          stored as rows: <code>GET /v1/incidents</code> from spec section 46 needs a
          projection over the evidence store that no phase has built, and listing
          fabricated summaries would be worse than asking for an id.
        </p>
      ) : (
        <Timeline correlationId={correlationId} />
      )}
    </Surface>
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
