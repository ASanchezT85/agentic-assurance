import { Surface } from "@/components/Surface";

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
 * So this page reads. Applying a control needs POST /v1/controls, which section 46
 * lists and no phase has built, and when it is built it belongs in the
 * customer-controlled plane rather than here.
 */
export default function ControlsPage() {
  return (
    <Surface
      title="Controls"
      summary="What is currently enforcing, and what shadow mode would have done."
    >
      <div style={{ border: "1px solid currentColor", borderRadius: "4px", padding: "1rem" }}>
        <strong>Not available from the console.</strong> The active policy bundle, the
        grant state and the shadow ledger all live in memory in the gateway and the
        fleet engine. None is persisted or exposed, so there is nothing to read.
        <p style={{ marginBottom: 0 }}>
          <code>POST /v1/controls</code> is not built here, and will not be.
        </p>
      </div>

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>Why there are no buttons</h2>
      <p>
        Spec section 59 forbids the web console from becoming required for execution,
        and section 17 requires production to be unaffected when it is down. A kill
        switch on this page would quietly invert that: the fastest way to stop trading
        would run through a service the architecture treats as optional.
      </p>
      <p>
        Fleet-level controls are shadow by default, and a customer authorizes
        enforcement rather than this system (INV-009). Whatever eventually applies a
        control belongs in the customer-controlled enforcement plane, behind an
        authorization that names its author and the policy bundle permitting it.
      </p>

      <h2 style={{ fontSize: "1rem", marginTop: "2rem" }}>The distinction this page keeps</h2>
      <p style={{ opacity: 0.75 }}>
        A recommendation is not an action. Shadow mode records what would have
        happened; only a customer authorization turns one into a control that binds.
        When this surface has data, those will be two columns and never one.
      </p>
    </Surface>
  );
}
