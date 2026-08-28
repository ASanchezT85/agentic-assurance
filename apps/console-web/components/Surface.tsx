import type { ReactNode } from "react";

/**
 * The shell every surface shares.
 *
 * Spec section 48 fixes six principal surfaces and says not to add more without a
 * defined acceptance requirement. The navigation is therefore a constant, not a
 * configurable list: adding a seventh means editing this array and having that show
 * up in review.
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
  children,
}: {
  title: string;
  summary: string;
  children: ReactNode;
}) {
  return (
    <main style={{ maxWidth: "68rem" }}>
      <nav style={{ display: "flex", gap: "1rem", flexWrap: "wrap", marginBottom: "2rem" }}>
        {SURFACES.map((s) => (
          <a key={s.href} href={s.href} style={{ textDecoration: "none" }}>
            {s.label}
          </a>
        ))}
      </nav>
      <h1 style={{ fontSize: "1.25rem", marginBottom: "0.25rem" }}>{title}</h1>
      <p style={{ marginTop: 0, opacity: 0.75 }}>{summary}</p>
      {children}
    </main>
  );
}

/**
 * What a surface renders when its data source is absent.
 *
 * It says why, and it never renders zeros. An empty fleet and an unreachable fleet
 * engine look identical on a dashboard that shows "0" for both, and only one of them
 * means the fleet is quiet.
 */
export function Unavailable({ reason }: { reason: string }) {
  return (
    <div
      style={{
        border: "1px solid currentColor",
        borderRadius: "4px",
        padding: "1rem",
        opacity: 0.85,
      }}
    >
      <strong>Not available.</strong> {reason}
      <p style={{ marginBottom: 0 }}>
        This is not an empty result. Nothing is being displayed because nothing could
        be read, and a zero here would be a different claim.
      </p>
    </div>
  );
}

/** A plain table. No charts: a chart of two data points is decoration. */
export function Table({
  columns,
  rows,
}: {
  columns: readonly string[];
  rows: readonly (readonly (string | number)[])[];
}) {
  if (rows.length === 0) {
    return <p>No rows in this window. The source answered; it had nothing to report.</p>;
  }
  return (
    <div style={{ overflowX: "auto" }}>
      <table style={{ borderCollapse: "collapse", width: "100%", fontSize: "0.85rem" }}>
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c} style={{ textAlign: "left", padding: "0.4rem 0.6rem", borderBottom: "1px solid currentColor" }}>
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={i}>
              {row.map((cell, j) => (
                <td key={j} style={{ padding: "0.4rem 0.6rem", borderBottom: "1px solid rgba(128,128,128,0.3)" }}>
                  {cell}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
