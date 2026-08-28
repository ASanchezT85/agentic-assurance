import { Surface, Unavailable } from "@/components/Surface";
import { evidenceFor } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Flow (spec section 48.2).
 *
 * Live intents with their authority and policy results need `GET /v1/intents`, which
 * spec section 46 lists and no phase has built: the submission path is not wired, so
 * there are no live intents to list.
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
        <p>
          Enter a correlation id. There is no list of live intents because{" "}
          <code>GET /v1/intents</code> does not exist yet: the submission path is not
          wired, so listing it would mean inventing rows.
        </p>
      ) : (
        <FlowChain correlationId={correlationId} />
      )}
    </Surface>
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
