import { Note, Pill, SourceStrip, Surface, Table, Unavailable } from "@/components/Surface";
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
      <form method="get" className="x-search">
        <label htmlFor="correlation_id">Correlation id</label>
        <input id="correlation_id" name="correlation_id" defaultValue={correlationId} />
        {/* Reading, not acting. This submits a query string; nothing on this page
            writes, and the only reason it is a button at all is that a search needs
            one. */}
        <button type="submit">Show chain</button>
      </form>

      {correlationId === "" ? <RecentIntents /> : <FlowChain correlationId={correlationId} />}

      <Note>
        Outcomes are printed as the enforcement plane recorded them. The console never
        re-derives a verdict: a refusal shown as anything other than the code that was
        written would be a second account of the same event, and the two can disagree.
      </Note>

      <Note>
        BUY and SELL are neutral here. Colouring a side would tell an operator that one
        direction is the dangerous one, which is a claim about a market rather than about
        this platform.
      </Note>
    </Surface>
  );
}

async function RecentIntents() {
  const result = await recentIntents();

  return (
    <>
      <SourceStrip
        source="GET /v1/intents?limit=50"
        availability={
          !result.available ? "unavailable" : result.rows.length === 0 ? "empty" : "live"
        }
        detail={
          result.available && result.rows.length > 0
            ? `${result.rows.length} recent intent${result.rows.length === 1 ? "" : "s"}`
            : undefined
        }
      />

      {!result.available ? (
        <Unavailable reason={result.reason} />
      ) : result.rows.length === 0 ? (
        <p className="x-empty">
          No intent has been submitted in this tenant in the last day.
        </p>
      ) : (
        <Table
          columns={["envelope", "intent", "last event", "outcome", "chain"]}
          rows={result.rows.map((i) => [
            <span key="e">
              <code>{i.envelope_id}</code>
              <div className="x-cell-sub">{i.received_at}</div>
            </span>,
            // Neutral. A side is a direction, not a severity.
            <span key="i">
              <span className="x-mono">{i.side}</span> {i.instrument_id}
            </span>,
            <code key="l">{i.last_event}</code>,
            // The code as recorded, never a verdict computed here.
            <span key="o" className="x-mono">
              {i.control ?? i.code ?? i.action ?? ""}
            </span>,
            <a key="c" href={`/flow?correlation_id=${encodeURIComponent(i.correlation_id)}`}>
              show
            </a>,
          ])}
        />
      )}
    </>
  );
}

async function FlowChain({ correlationId }: { correlationId: string }) {
  const chain = await evidenceFor(correlationId);

  return (
    <>
      <SourceStrip
        source={`GET /v1/evidence?correlation_id=${correlationId}`}
        availability={
          !chain.available ? "unavailable" : chain.rows.length === 0 ? "empty" : "live"
        }
        detail={
          chain.available && chain.rows.length > 0
            ? `${chain.rows.length} event${chain.rows.length === 1 ? "" : "s"}`
            : undefined
        }
      />

      {!chain.available ? (
        <Unavailable reason={chain.reason} />
      ) : chain.rows.length === 0 ? (
        <p className="x-empty">No evidence for that correlation id in this tenant.</p>
      ) : (
        <ol className="x-timeline">
          {chain.rows.map((event) => (
            <li key={event.event_id}>
              <div className="x-timeline-head">
                <span className="x-mono">{event.occurred_at}</span>
                <code>{event.event_name}</code>
                {/* A correction is its own kind of event, and the data language keeps it
                    apart from a plain record for a reason: the original stays in the
                    chain above, exactly as it was written, and a reader has to be able
                    to see that both are there. */}
                {event.corrects_event_id ? <Pill tone="warning">CORRECTION</Pill> : null}
              </div>
              <div className="x-cell-sub">
                produced by {event.producer}, sequence {event.sequence}
                {event.causation_id ? `, caused by ${event.causation_id}` : ""}
              </div>
              {event.corrects_event_id ? (
                <div className="x-correction">
                  Corrects <code>{event.corrects_event_id}</code>. The earlier event is
                  still in the chain above, exactly as it was recorded — a correction adds
                  to the account rather than replacing it.
                </div>
              ) : null}
              {event.payload ? (
                <details className="x-payload">
                  <summary>payload</summary>
                  <pre>{JSON.stringify(event.payload, null, 2)}</pre>
                </details>
              ) : null}
            </li>
          ))}
        </ol>
      )}
    </>
  );
}
