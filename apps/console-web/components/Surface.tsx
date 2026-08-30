import type { ReactNode } from "react";

/**
 * The shell every surface shares.
 *
 * Spec section 48 fixes six principal surfaces and says not to add more without a
 * defined acceptance requirement. The navigation is therefore a constant, not a
 * configurable list: adding a seventh means editing this array and having that show
 * up in review. The design authority repeats the rule, so it now has two places to be
 * caught in and none to be added quietly.
 */
export const SURFACES = [
  { href: "/fleet", label: "Fleet" },
  { href: "/flow", label: "Flow" },
  { href: "/dependencies", label: "Dependencies" },
  { href: "/incidents", label: "Incidents" },
  { href: "/lab", label: "Lab" },
  { href: "/controls", label: "Controls" },
] as const;

export function Surface({
  title,
  summary,
  current,
  children,
}: {
  title: string;
  summary: string;
  /** The surface's own href, so the navigation can mark where the reader is. */
  current?: string;
  children: ReactNode;
}) {
  return (
    <div className="x-shell">
      <header className="x-masthead">
        <span className="x-wordmark">
          {/* eslint-disable-next-line @next/next/no-img-element -- a 2 KB SVG master;
              next/image would put a raster optimizer in front of a vector. */}
          {/* Artwork, placed as artwork. The wordmark is never re-typed in a display
              font — the one thing the brand authority is most explicit about. */}
          <img src="/exoryn-primary-horizontal.svg" alt="EXORYN" />
        </span>
        {/* Stated, not implied. The Console observes; it never becomes the thing that
            executes, and an operator should be able to read that from the page rather
            than infer it from the absence of buttons. */}
        <span className="x-readonly">read-only</span>
      </header>

      <nav className="x-nav" aria-label="Console surfaces">
        {SURFACES.map((s) => (
          <a key={s.href} href={s.href} aria-current={s.href === current ? "page" : undefined}>
            {s.label}
          </a>
        ))}
      </nav>

      <main>
        <h1 className="x-surface-title">{title}</h1>
        <p className="x-surface-summary">{summary}</p>
        {children}
      </main>

      <footer className="x-footnote">
        EXORYN — assurance infrastructure for autonomous finance. This console reads the
        platform&apos;s own record; it does not authorize, submit or cancel anything.
      </footer>
    </div>
  );
}

/**
 * What a surface renders when its data source is absent.
 *
 * It says why, and it never renders zeros. An empty fleet and an unreachable fleet
 * engine look identical on a dashboard that shows "0" for both, and only one of them
 * means the fleet is quiet.
 *
 * Styled with the warning palette rather than the danger palette: an unreadable source is
 * a degradation, not an incident, and reserving red for incidents is what keeps red
 * meaning something.
 */
export function Unavailable({ reason }: { reason: string }) {
  return (
    <div className="x-unavailable" role="status">
      <strong>Not available.</strong> {reason}
      <p>
        This is not an empty result. Nothing is being displayed because nothing could
        be read, and a zero here would be a different claim.
      </p>
    </div>
  );
}

/** A panel groups one region of a surface. */
export function Panel({ title, children }: { title?: string; children: ReactNode }) {
  return (
    <section className="x-panel">
      {title ? <h3>{title}</h3> : null}
      {children}
    </section>
  );
}

/**
 * A status pill.
 *
 * The tone is chosen by the caller from a closed set, because the data language assigns
 * meanings to these colours — verified, declared, unknown, and the three severities — and
 * a surface picking its own would be inventing a meaning. The label is always readable
 * text: colour-only status is forbidden by the accessibility foundation, and it is also
 * simply worse, because an operator reading a screenshot in a ticket has no legend.
 */
export type PillTone =
  | "verified"
  | "declared"
  | "unknown"
  | "success"
  | "warning"
  | "danger";

export function Pill({ tone, children }: { tone: PillTone; children: ReactNode }) {
  return <span className={`x-pill x-pill--${tone}`}>{children}</span>;
}

/**
 * The availability of a surface's source, stated rather than implied.
 *
 * The data language keeps four states apart, and three of them are derivable from what
 * the API actually answered: the source could not be read (UNAVAILABLE), it answered with
 * nothing (EMPTY), or it answered with rows (LIVE).
 *
 * STALE is deliberately absent. It means "answered, but older than this surface's
 * freshness threshold", and no such threshold is defined anywhere in the platform. Adding
 * one here would invent an operational contract in a stylesheet, and rendering LIVE for
 * data that is in fact old is the smaller error than inventing the boundary between them.
 */
export type Availability = "live" | "empty" | "unavailable";

export function SourceStrip({
  source,
  availability,
  detail,
}: {
  /** The endpoint this surface reads. Named, so an operator knows where to look. */
  source: string;
  availability: Availability;
  detail?: string | undefined;
}) {
  const tone: PillTone = availability === "unavailable" ? "warning" : "unknown";
  const label =
    availability === "unavailable" ? "UNAVAILABLE" : availability === "empty" ? "EMPTY" : "LIVE";

  return (
    <div className="x-source">
      <Pill tone={tone}>{label}</Pill>
      <code>{source}</code>
      {detail ? <span className="x-source-detail">{detail}</span> : null}
    </div>
  );
}

/**
 * A note about what a surface deliberately does not show.
 *
 * These exist on every surface and they are not decoration: each one records a place
 * where the obvious visualisation would state more than the platform knows.
 */
export function Note({ children }: { children: ReactNode }) {
  return <p className="x-note">{children}</p>;
}

/** A plain table. No charts: a chart of two data points is decoration. */
export function Table({
  columns,
  rows,
}: {
  columns: readonly string[];
  rows: readonly (readonly (string | number | ReactNode)[])[];
}) {
  if (rows.length === 0) {
    return (
      <p className="x-empty">
        No rows in this window. The source answered; it had nothing to report.
      </p>
    );
  }
  return (
    <div className="x-table-wrap">
      <table className="x-table">
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c}>{c}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j}>{cell}</td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
