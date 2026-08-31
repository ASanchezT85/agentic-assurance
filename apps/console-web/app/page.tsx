import { AppShell, Panel, SURFACES } from "@/components/Surface";

/**
 * The console home.
 *
 * Phase 14 opened the six surfaces of MASTER_BUILD_SPEC.md section 48, which is when
 * ADR-017 said the scaffold guard comes off. There are six, and there will be six:
 * section 48 ends by saying not to add dashboards without a defined acceptance
 * requirement, and the design authority repeats it.
 *
 * It shares the application shell with every surface rather than drawing its own
 * masthead. Two shells is two places for the brand, the navigation and the read-only
 * statement to drift apart, and the read-only statement is the one that must not.
 */
export default function Home() {
  return (
    <AppShell context="Console">
      <h1 className="x-surface-title">Control before execution.</h1>
      <p className="x-surface-summary">
        Assurance infrastructure for autonomous finance. This console reads the
        platform&apos;s own record of what its agents asked for, what was authorized and
        what reached a venue.
      </p>

      <Panel title="What this console is">
        <p>
          It reads. It has no write path, and production is unaffected when it is down
          (spec section 17, section 59). Nothing here authorizes, submits or cancels
          anything: enforcement happens in the gateway, and a console that could change
          what the platform permits would be a second place to look for the answer.
        </p>
        <p>
          Where a surface has no endpoint behind it yet, it says so and names what is
          missing. It does not render placeholder rows: an empty result and an absent
          source are different facts, and only one of them is about the fleet.
        </p>
        <p style={{ marginBottom: 0 }}>
          <strong>Real-money execution is not supported.</strong> This platform does not
          generate investment recommendations.
        </p>
      </Panel>

      <Panel title="The six surfaces">
        <ul className="x-surface-list">
          {SURFACES.map((s) => (
            <li key={s.href}>
              <a href={s.href}>{s.label}</a>
            </li>
          ))}
        </ul>
      </Panel>

      <p className="x-empty">
        Specification: <code>MASTER_BUILD_SPEC.md</code> at the repository root. Design
        system: <code>design/exoryn/</code>.
      </p>
    </AppShell>
  );
}
