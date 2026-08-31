import Link from "next/link";
import type { ReactNode } from "react";

/**
 * The shell every surface shares.
 *
 * Spec section 48 fixes six principal surfaces and says not to add more without a
 * defined acceptance requirement. The navigation is therefore a constant, not a
 * configurable list: adding a seventh means editing this array and having that show
 * up in review. The design authority repeats the rule, so it now has two places to be
 * caught in and none to be added quietly.
 *
 * The layout is the canonical one from design/exoryn/07-web-patterns: a fixed compact
 * left navigation, a 64 px top bar, a surface header, a summary region and then the
 * operational table. It is not a dashboard of cards, because this product's job is
 * exact inspection and a card that rounds a number is a card that lies about it.
 *
 * The icons are geometry, not decoration: each one is a plain figure that survives at
 * 16 px and in monochrome. No glyph font is loaded — a navigation that depends on a
 * network to be legible is a navigation that is illegible on a bad day.
 */
export const SURFACES = [
  { href: "/fleet", label: "Fleet", icon: "fleet" },
  { href: "/flow", label: "Flow", icon: "flow" },
  { href: "/dependencies", label: "Dependencies", icon: "dependencies" },
  { href: "/incidents", label: "Incidents", icon: "incidents" },
  { href: "/lab", label: "Lab", icon: "lab" },
  { href: "/controls", label: "Controls", icon: "controls" },
] as const;

function NavIcon({ name }: { name: string }) {
  const common = {
    width: 16,
    height: 16,
    viewBox: "0 0 16 16",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.4,
    strokeLinecap: "round" as const,
    strokeLinejoin: "round" as const,
    "aria-hidden": true,
  };
  switch (name) {
    case "fleet": // a population, measured
      return (
        <svg {...common}>
          <circle cx="4" cy="4" r="1.6" />
          <circle cx="12" cy="4" r="1.6" />
          <circle cx="4" cy="12" r="1.6" />
          <circle cx="12" cy="12" r="1.6" />
          <path d="M5.6 4h4.8M4 5.6v4.8M12 5.6v4.8M5.6 12h4.8" />
        </svg>
      );
    case "flow": // one intent, moving through
      return (
        <svg {...common}>
          <path d="M2 8h9" />
          <path d="M8.5 4.5 12 8l-3.5 3.5" />
          <path d="M14 3v10" />
        </svg>
      );
    case "dependencies": // many resting on one
      return (
        <svg {...common}>
          <circle cx="8" cy="3.5" r="1.6" />
          <circle cx="3" cy="12.5" r="1.6" />
          <circle cx="8" cy="12.5" r="1.6" />
          <circle cx="13" cy="12.5" r="1.6" />
          <path d="M8 5.1v2.4M8 7.5H3.6a.6.6 0 0 0-.6.6v2.8M8 7.5h4.4a.6.6 0 0 1 .6.6v2.8M8 7.5v3.4" />
        </svg>
      );
    case "incidents": // an alert, not a skull
      return (
        <svg {...common}>
          <path d="M8 2.5 14.5 13.5h-13Z" />
          <path d="M8 6.8v3" />
          <path d="M8 11.6h.01" />
        </svg>
      );
    case "lab": // a repeatable experiment
      return (
        <svg {...common}>
          <path d="M6.5 2v4.2L3 12.2A1 1 0 0 0 3.9 13.7h8.2a1 1 0 0 0 .9-1.5L9.5 6.2V2" />
          <path d="M5.5 2h5" />
          <path d="M5.2 9.6h5.6" />
        </svg>
      );
    case "controls": // a boundary that is either in force or not
      return (
        <svg {...common}>
          <rect x="2.5" y="6.5" width="11" height="7" rx="1.2" />
          <path d="M5.5 6.5V4.8a2.5 2.5 0 0 1 5 0v1.7" />
          <path d="M8 9.4v1.6" />
        </svg>
      );
    default:
      return null;
  }
}

/**
 * The application frame: left navigation, top bar, content.
 *
 * Used by every surface and by the console home, so the brand, the read-only statement
 * and the navigation cannot drift between them.
 */
export function AppShell({
  current,
  context,
  children,
}: {
  /** The surface's own href, so the navigation can mark where the reader is. */
  current?: string | undefined;
  /** What the top bar names as the reader's position. */
  context: string;
  children: ReactNode;
}) {
  return (
    <div className="x-app">
      <aside className="x-rail">
        <Link className="x-rail-brand" href="/" aria-label="EXORYN — console home">
          {/* Artwork, placed as artwork. The wordmark is never re-typed in a display
              font — the one thing the brand authority is most explicit about, and
              next/image would put a raster optimizer in front of a 2 KB vector. */}
          {/* Two marks, one identity. Below 1024 px the rail is 64 px wide and the
              horizontal wordmark cannot fit in it legibly; the brand's own usage rules
              name the icon as the mark for compact product surfaces, so that is what
              appears there. Neither file is redrawn — /favicon.svg is byte-identical to
              the canonical exoryn-icon-primary.svg. */}
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img className="x-mark-wide" src="/exoryn-primary-horizontal.svg" alt="EXORYN" />
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img className="x-mark-compact" src="/favicon.svg" alt="EXORYN" />
        </Link>

        <nav className="x-rail-nav" aria-label="Console surfaces">
          {SURFACES.map((s) => (
            <Link
              key={s.href}
              href={s.href}
              aria-current={s.href === current ? "page" : undefined}
              title={s.label}
            >
              <NavIcon name={s.icon} />
              <span>{s.label}</span>
            </Link>
          ))}
        </nav>

        <div className="x-rail-foot">
          <p>
            Reads the platform&apos;s own record. It does not authorize, submit or cancel
            anything.
          </p>
        </div>
      </aside>

      <div className="x-frame">
        <header className="x-topbar">
          <span className="x-topbar-context">{context}</span>
          <span className="x-topbar-flags">
            {/* Stated, not implied. The Console observes; it never becomes the thing
                that executes, and an operator should be able to read that from the page
                rather than infer it from the absence of buttons. */}
            <span className="x-flag">read-only</span>
            <span className="x-flag">paper only</span>
          </span>
        </header>

        <main className="x-content">{children}</main>

        <footer className="x-footnote">
          EXORYN — assurance infrastructure for autonomous finance. This console reads the
          platform&apos;s own record; it does not authorize, submit or cancel anything.
        </footer>
      </div>
    </div>
  );
}

export function Surface({
  title,
  summary,
  current,
  children,
}: {
  title: string;
  summary: string;
  current?: string | undefined;
  children: ReactNode;
}) {
  return (
    <AppShell current={current} context={title}>
      <h1 className="x-surface-title">{title}</h1>
      <p className="x-surface-summary">{summary}</p>
      {children}
    </AppShell>
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

/**
 * The fourth state the data language names: the answer has been asked for and has not
 * arrived yet.
 *
 * It renders as neither data nor absence. A skeleton that looked like a table of rows
 * would be a table of rows the platform has not read, which is the one thing every other
 * component here exists to avoid — so this says what it is in words, and the bars carry
 * no numbers, no columns and no row count.
 */
export function Loading({ what }: { what: string }) {
  return (
    <div className="x-loading" role="status" aria-live="polite">
      <p>
        <strong>Reading {what}.</strong> Nothing below is a measurement yet.
      </p>
      <span className="x-loading-bars" aria-hidden="true">
        <i />
        <i />
        <i />
      </span>
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
 * nothing (EMPTY), or it answered with rows (LIVE). The fourth, LOADING, belongs to the
 * render before the answer arrives and is the `Loading` component above.
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
