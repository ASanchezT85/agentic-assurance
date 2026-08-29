import { Surface, Unavailable } from "@/components/Surface";
import { controls } from "@/lib/api";

export const dynamic = "force-dynamic";

/**
 * Controls (spec section 48.6).
 *
 * This is the surface most likely to grow a button, and it deliberately has none.
 *
 * The console is never required for execution (section 59; section 17: "Console
 * unavailable -> Production execution unaffected"). A kill switch here would make the
 * console operationally load-bearing, which is the dependency the whole enforcement
 * plane exists to avoid: the customer's gateway has to be able to stop things with
 * this service down and our cloud gone.
 *
 * So this page reads. It used to say there was nothing to read, because controls lived
 * in memory and no endpoint exposed them — and it kept saying so after POST/GET
 * /v1/controls landed, which is the stale-caveat failure the API docs already made
 * twice: prose describing a system nobody is running.
 */
export default function ControlsPage() {
  return (
    <Surface
      title="Controls"
      summary="What is currently enforcing, and what shadow mode would have done."
    >
      <ControlList />

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>Why there are no buttons</h2>
      <p>
        Spec section 59 forbids the web console from becoming required for execution,
        and section 17 requires production to be unaffected when it is down. A kill
        switch on this page would quietly invert that: the fastest way to stop trading
        would run through a service the architecture treats as optional.
      </p>
      <p>
        Authorizing and lifting a control are <code>POST /v1/controls</code> and{" "}
        <code>POST /v1/controls/&#123;id&#125;/revoke</code> on the gateway, in the
        customer-controlled enforcement plane, behind a credential that may not also
        submit orders. They are not built here and will not be.
      </p>

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>The distinction this page keeps</h2>
      <p style={{ opacity: 0.75 }}>
        A recommendation is not an action. Shadow mode records what would have
        happened; only a customer authorization turns one into a control that binds
        (INV-009). What is listed above bound something.
      </p>
    </Surface>
  );
}

async function ControlList() {
  const result = await controls();
  if (!result.available) {
    return <Unavailable reason={result.reason} />;
  }
  if (result.rows.length === 0) {
    return (
      <p>
        No fleet control has been authorized in this tenant. That is the default state:
        fleet intelligence recommends and nothing binds until a customer authorizes it.
      </p>
    );
  }

  return (
    <table style={{ borderCollapse: "collapse", width: "100%" }}>
      <thead>
        <tr style={{ textAlign: "left" }}>
          <th>Control</th>
          <th>Action</th>
          <th>Scope</th>
          <th>Authorized by</th>
          <th>Until</th>
          <th>State</th>
        </tr>
      </thead>
      <tbody>
        {result.rows.map((c) => (
          <tr key={c.control_id} style={{ borderTop: "1px solid currentColor" }}>
            <td>
              <code>{c.control_id}</code>
              <div style={{ opacity: 0.75 }}>{c.reason}</div>
            </td>
            <td>{c.action}</td>
            <td>{c.agent_id || c.account_id || "whole tenant"}</td>
            <td>{c.authorized_by}</td>
            <td>{c.expires_at}</td>
            {/* in_force comes from the gateway rather than from a comparison here: a
                reader must never have to check an expiry against their own clock to
                know whether orders are being refused right now. */}
            <td>{c.in_force ? "enforcing" : c.revoked_at ? `revoked by ${c.revoked_by}` : "expired"}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
