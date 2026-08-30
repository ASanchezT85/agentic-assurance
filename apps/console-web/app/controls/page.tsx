import { Note, Panel, Pill, SourceStrip, Surface, Table, Unavailable } from "@/components/Surface";
import { controls, type ControlRow } from "@/lib/api";

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
export default async function ControlsPage() {
  const result = await controls();

  // In force first, then history. The design specification asks for that order and the
  // reason is operational: an operator arriving at this page is asking what binds right
  // now, and a revoked control from last week sitting above it is an answer to a
  // different question.
  const inForce = result.available ? result.rows.filter((c) => c.in_force) : [];
  const historical = result.available ? result.rows.filter((c) => !c.in_force) : [];

  return (
    <Surface
      current="/controls"
      title="Controls"
      summary="What is currently enforcing, and what shadow mode would have done."
    >
      <SourceStrip
        source="GET /v1/controls"
        availability={
          !result.available ? "unavailable" : result.rows.length === 0 ? "empty" : "live"
        }
        detail={
          result.available && result.rows.length > 0
            ? `${inForce.length} enforcing, ${historical.length} historical`
            : undefined
        }
      />

      {!result.available ? (
        <Unavailable reason={result.reason} />
      ) : result.rows.length === 0 ? (
        <p className="x-empty">
          No fleet control has been authorized in this tenant. That is the default state:
          fleet intelligence recommends and nothing binds until a customer authorizes it.
        </p>
      ) : (
        <>
          <Panel title="Enforcing now">
            {inForce.length === 0 ? (
              <p className="x-empty" style={{ marginBottom: 0 }}>
                Nothing binds at the moment. Every control below has been revoked or has
                expired.
              </p>
            ) : (
              <ControlTable rows={inForce} />
            )}
          </Panel>

          {historical.length > 0 ? (
            <Panel title="No longer binding">
              <ControlTable rows={historical} />
            </Panel>
          ) : null}
        </>
      )}

      <Panel title="Why there are no buttons">
        <p>
          Spec section 59 forbids the web console from becoming required for execution,
          and section 17 requires production to be unaffected when it is down. A kill
          switch on this page would quietly invert that: the fastest way to stop trading
          would run through a service the architecture treats as optional.
        </p>
        <p style={{ marginBottom: 0 }}>
          Authorizing and lifting a control are <code>POST /v1/controls</code> and{" "}
          <code>POST /v1/controls/&#123;id&#125;/revoke</code> on the gateway, in the
          customer-controlled enforcement plane, behind a credential that may not also
          submit orders. They are not built here and will not be.
        </p>
      </Panel>

      <Note>
        A recommendation is not an action. Shadow mode records what would have happened;
        only a customer authorization turns one into a control that binds (INV-009). Every
        row above bound something, or bound something once — this surface never lists a
        recommendation, so nothing here can be mistaken for one.
      </Note>

      <Note>
        The active policy bundle, grant state and audit history are not shown. The Master
        Spec describes them as part of Controls and no read model exposes them together;
        panels assembled from what the console can reach would look like the platform&apos;s
        answer without being it.
      </Note>
    </Surface>
  );
}

function ControlTable({ rows }: { rows: readonly ControlRow[] }) {
  return (
    <Table
      columns={["control", "action", "scope", "authorized by", "until", "state"]}
      rows={rows.map((c) => [
        <span key="id">
          <code>{c.control_id}</code>
          <div className="x-cell-sub">{c.reason}</div>
        </span>,
        <code key="a">{c.action}</code>,
        // A list-scoped control rendered as "whole tenant" would tell an operator their
        // entire customer is stopped when two agents are.
        <span key="s" className="x-mono">
          {scopeOf(c)}
        </span>,
        c.authorized_by,
        <span key="u" className="x-mono">
          {c.expires_at}
        </span>,
        // in_force comes from the gateway rather than from a comparison here: a reader
        // must never have to check an expiry against their own clock to know whether
        // orders are being refused right now.
        c.in_force ? (
          <Pill key="st" tone="danger">
            ENFORCING
          </Pill>
        ) : c.revoked_at ? (
          <span key="st">
            <Pill tone="unknown">REVOKED</Pill>
            <div className="x-cell-sub">by {c.revoked_by}</div>
          </span>
        ) : (
          <Pill key="st" tone="unknown">
            EXPIRED
          </Pill>
        ),
      ])}
    />
  );
}

function scopeOf(c: ControlRow): string {
  if (c.agent_id) return c.agent_id;
  if (c.agent_ids && c.agent_ids.length > 0) return c.agent_ids.join(", ");
  if (c.account_id) return c.account_id;
  return "whole tenant";
}
