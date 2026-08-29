import { Surface, Unavailable } from "@/components/Surface";
import { evidenceFor, recentIntents } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Flow (spec section 48.2).
 *
 * Listing live intents needs a `GET /v1/intents` collection, which nothing serves: the
 * submission path is wired and every intent it decides is in the evidence store, but
 * there is no endpoint that returns them as a list. This comment used to say the
 * submission path was not wired at all, which stopped being true and kept being read.
 *
 * What does exist is the evidence chain, so this surface reads that. Paste a
 * correlation id and the page walks agent → intent → authority → policy → broker
 * order → result, which is spec section 66 step 19 with a browser instead of curl.
 */
export default async function FlowPage({
  searchParams,
}: {
  searchParams: Promise<{ correlation_id?: string }>;
}) {
  const params = await searchParams;
  const correlationId = params.correlation_id?.trim() ?? "";

  return (
    <Surface
      current="/flow"
      title="Flow"
      summary="The evidence chain for one correlation id, exactly as recorded."
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
          Show chain
        </button>
      </form>

      {correlationId === "" ? (
        <RecentIntents />
      ) : (
        <FlowChain correlationId={correlationId} />
      )}
    </Surface>
  );
}

async function RecentIntents() {
  const result = await recentIntents();
  if (!result.available) {
    return <Unavailable reason={result.reason} />;
  }
  if (result.rows.length === 0) {
    return <p>No intent has been submitted in this tenant in the last day.</p>;
  }

  return (
    <table style={{ borderCollapse: "collapse", width: "100%" }}>
      <thead>
        <tr style={{ textAlign: "left" }}>
          <th>Envelope</th>
          <th>Intent</th>
          <th>Last event</th>
          <th>Outcome</th>
          <th>Chain</th>
        </tr>
      </thead>
      <tbody>
        {result.rows.map((i) => (
          <tr key={i.envelope_id} style={{ borderTop: "1px solid currentColor" }}>
            <td>
              <code>{i.envelope_id}</code>
              <div style={{ opacity: 0.75 }}>{i.received_at}</div>
            </td>
            <td>
              {i.side} {i.instrument_id}
            </td>
            <td>{i.last_event}</td>
            {/* The code as recorded, never a verdict computed here. A refusal shown
                as anything other than what the enforcement plane wrote is a second
                account of the same event, and the two can disagree. */}
            <td>{i.control ?? i.code ?? i.action ?? ""}</td>
            <td>
              <a href={`/flow?correlation_id=${encodeURIComponent(i.correlation_id)}`}>
                show
              </a>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

async function FlowChain({ correlationId }: { correlationId: string }) {
  const chain = await evidenceFor(correlationId);
  if (!chain.available) {
    return <Unavailable reason={chain.reason} />;
  }
  if (chain.rows.length === 0) {
    return <p>No evidence for that correlation id in this tenant.</p>;
  }

  return (
    <ol style={{ paddingLeft: "1.2rem" }}>
      {chain.rows.map((event) => (
        <li key={event.event_id} style={{ marginBottom: "0.9rem" }}>
          <div>
            <strong>{event.occurred_at}</strong> {event.event_name}
          </div>
          <div style={{ opacity: 0.75 }}>
            produced by {event.producer}, sequence {event.sequence}
            {event.causation_id ? `, caused by ${event.causation_id}` : ""}
          </div>
          {event.corrects_event_id ? (
            <div>
              <strong>Correction</strong> of {event.corrects_event_id}. The earlier
              event is still in the chain above, exactly as it was recorded.
            </div>
          ) : null}
          {event.payload ? (
            <pre style={{ margin: "0.3rem 0 0", fontSize: "0.75rem", whiteSpace: "pre-wrap" }}>
              {JSON.stringify(event.payload, null, 2)}
            </pre>
          ) : null}
        </li>
      ))}
    </ol>
  );
}
