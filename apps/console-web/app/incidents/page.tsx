import { Note, Pill, SourceStrip, Surface, Table, Unavailable } from "@/components/Surface";
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
      current="/incidents"
      title="Incidents"
      summary="Timelines rebuilt from the append-only record. If this disagrees with anything, this is right."
    >
      <form method="get" className="x-search">
        <label htmlFor="correlation_id">Correlation id</label>
        <input id="correlation_id" name="correlation_id" defaultValue={correlationId} />
        <button type="submit">Rebuild timeline</button>
      </form>

      {correlationId === "" ? <OpenIncidents /> : <Timeline correlationId={correlationId} />}

      <Note>
        A recommendation is labelled as one. The platform recommends and a customer
        authorizes (INV-009), so nothing on this page may read as an applied control
        unless the evidence says it was enforced — and the Controls surface, not this
        one, lists what binds.
      </Note>

      <Note>
        Cohort, shared dependencies and the controls that followed are not shown as
        context panels. They would need fields the incident projection does not carry,
        and a panel filled from what the console could reach would look like the
        platform&apos;s account of the incident without being it.
      </Note>
    </Surface>
  );
}

async function OpenIncidents() {
  const result = await incidents();

  return (
    <>
      <SourceStrip
        source="GET /v1/incidents"
        availability={
          !result.available ? "unavailable" : result.rows.length === 0 ? "empty" : "live"
        }
        detail={
          result.available && result.rows.length > 0
            ? `${result.rows.length} incident${result.rows.length === 1 ? "" : "s"}`
            : undefined
        }
      />

      {!result.available ? (
        <Unavailable reason={result.reason} />
      ) : result.rows.length === 0 ? (
        <p className="x-empty">
          No incident has been opened in this tenant. Enter a correlation id above to
          rebuild a timeline from evidence anyway — the chain outlives the projection.
        </p>
      ) : (
        <Table
          columns={["opened", "severity", "why", "recommended", "timeline"]}
          rows={result.rows.map((i) => [
            <span key="o" className="x-mono">
              {i.opened_at}
            </span>,
            <span key="s">
              <Severity severity={i.severity} />
              <div className="x-cell-sub">{i.severity_rule}</div>
            </span>,
            i.anomaly_rules.join(", "),
            // Recommended, never "applied". The platform recommends and a customer
            // authorizes (INV-009); the Controls surface lists what actually binds.
            <span key="r">
              <Pill tone="unknown">RECOMMENDED ONLY</Pill>
              <div className="x-cell-sub">{i.recommended}</div>
            </span>,
            <a key="t" href={`/incidents?correlation_id=${encodeURIComponent(i.correlation_id)}`}>
              rebuild
            </a>,
          ])}
        />
      )}
    </>
  );
}

/**
 * Severity is categorical, and it is rendered as a category.
 *
 * Not a bar, not a percentage, not a colour on its own: the data language says these are
 * named bands, and a scale would invite an operator to read a distance between two of
 * them that the platform never measured.
 */
function Severity({ severity }: { severity: string }) {
  const normalized = severity.toUpperCase();
  const tone =
    normalized === "CRITICAL" || normalized === "HIGH"
      ? "danger"
      : normalized === "MEDIUM"
        ? "warning"
        : "unknown";
  return <Pill tone={tone}>{normalized}</Pill>;
}

async function Timeline({ correlationId }: { correlationId: string }) {
  const chain = await evidenceFor(correlationId);

  const relevant = chain.available
    ? chain.rows.filter(
        (e) => e.event_name.startsWith("incident.") || e.event_name.startsWith("control."),
      )
    : [];
  const ordered = [...relevant].sort((a, b) => a.occurred_at.localeCompare(b.occurred_at));

  return (
    <>
      <SourceStrip
        source={`GET /v1/evidence?correlation_id=${correlationId}`}
        availability={!chain.available ? "unavailable" : ordered.length === 0 ? "empty" : "live"}
        detail={
          chain.available && ordered.length > 0
            ? `${ordered.length} incident event${ordered.length === 1 ? "" : "s"} of ${chain.rows.length} in the chain`
            : undefined
        }
      />

      {!chain.available ? (
        <Unavailable reason={chain.reason} />
      ) : ordered.length === 0 ? (
        <p className="x-empty">
          No incident evidence for that correlation id in this tenant.
        </p>
      ) : (
        <ol className="x-timeline">
          {ordered.map((event) => {
            const payload = event.payload ?? {};
            const enforced = payload["enforced"] === true;
            const actor = typeof payload["actor"] === "string" ? payload["actor"] : "";
            const isControl = event.event_name.startsWith("control.");

            return (
              <li key={event.event_id}>
                <div className="x-timeline-head">
                  <span className="x-mono">{event.occurred_at}</span>
                  <code>{event.event_name}</code>
                  {/* The distinction the whole surface exists for, stated on the event
                      itself rather than left to the reader to assemble from a payload. */}
                  {isControl ? (
                    enforced ? (
                      <Pill tone="danger">ENFORCING</Pill>
                    ) : (
                      <Pill tone="unknown">RECOMMENDED ONLY</Pill>
                    )
                  ) : null}
                </div>

                {isControl ? (
                  <div className="x-cell-sub">
                    {enforced ? (
                      <>A control that took effect{actor ? `, authorized by ${actor}` : ""}.</>
                    ) : (
                      <>
                        Nothing was enforced. Customer policy authorizes enforcement, not
                        this system.
                      </>
                    )}
                  </div>
                ) : null}

                {actor && !isControl ? (
                  <div className="x-cell-sub">Human action by {actor}.</div>
                ) : null}

                <details className="x-payload">
                  <summary>payload</summary>
                  <pre>{JSON.stringify(payload, null, 2)}</pre>
                </details>
              </li>
            );
          })}
        </ol>
      )}
    </>
  );
}
