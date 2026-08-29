/**
 * The console's only route to data.
 *
 * Two rules shape this file.
 *
 * The console is never required for execution (spec section 59, section 17: "Console
 * unavailable -> Production execution unaffected"). It reads; it has no write path,
 * and there is no function here that posts anything.
 *
 * And it never invents data. A surface with no endpoint behind it says so, naming the
 * phase that delivers it, rather than rendering a plausible-looking placeholder.
 * Section 28 forbids collapsing low-confidence data into a precise display, and a
 * mocked screen is the extreme case of that.
 */

export const GATEWAY_URL = process.env.GATEWAY_URL ?? "http://localhost:8080";
export const FLEET_ENGINE_URL = process.env.FLEET_ENGINE_URL ?? "http://localhost:8081";

/**
 * The console's credential.
 *
 * The tenant used to be a header and this comment used to say that authentication
 * would arrive with the surface that carried it. It arrived: every endpoint that
 * returns tenant data authenticates, and the tenant comes from the credential rather
 * than from anything the console sends. A header naming a tenant now gets 401.
 *
 * Server-side only. It is read in a server component's fetch and never reaches the
 * browser, which is why it is CONSOLE_API_TOKEN and not NEXT_PUBLIC_ anything: a
 * credential in a client bundle is a credential published.
 */
const API_TOKEN = process.env.CONSOLE_API_TOKEN ?? "";

export type Unavailable = {
  readonly available: false;
  /** Why there is nothing to show. Never "no data": an empty result and an absent
   * endpoint are different facts, and only one of them is about the fleet. */
  readonly reason: string;
};

export type Available<T> = {
  readonly available: true;
  readonly rows: readonly T[];
  readonly count: number;
};

export type Result<T> = Available<T> | Unavailable;

async function read<T>(url: string, what: string, key = "rows"): Promise<Result<T>> {
  try {
    if (API_TOKEN === "") {
      return {
        available: false,
        reason: `${what} needs CONSOLE_API_TOKEN: every endpoint that carries tenant data authenticates`,
      };
    }

    const response = await fetch(url, {
      headers: { Authorization: `Bearer ${API_TOKEN}` },
      // Always fresh. A cached fleet view is a fleet view of the past presented as
      // the present.
      cache: "no-store",
    });

    if (response.status === 503) {
      return { available: false, reason: `${what} is unavailable: the store behind it is not reachable` };
    }
    if (response.status === 401 || response.status === 403) {
      // Said plainly rather than as a bare status. An operator seeing "returned 401"
      // on a fleet screen would reasonably wonder whether the fleet was the problem.
      return {
        available: false,
        reason: `${what} refused the console's credential (${response.status}); check CONSOLE_API_TOKEN`,
      };
    }
    if (!response.ok) {
      return { available: false, reason: `${what} returned ${response.status}` };
    }

    // The collection key varies by endpoint (rows, incidents, controls) and is named
    // by the caller. Defaulting to the endpoint's own noun keeps the payloads
    // readable; guessing would turn a renamed field into an empty screen that looks
    // like a quiet fleet.
    const body = (await response.json()) as Record<string, unknown>;
    const rows = (body[key] as T[] | undefined) ?? [];
    return { available: true, rows, count: (body.count as number | undefined) ?? rows.length };
  } catch {
    // The service is down. That is a fact worth showing rather than an error to
    // swallow: an operator needs to know the difference between "quiet fleet" and
    // "nothing is reporting".
    return { available: false, reason: `${what} could not be reached` };
  }
}

export type FleetMeasurement = {
  cohort_id: string;
  cohort_predicate: string;
  window_start: string;
  window_end: string;
  intent_count: number;
  agent_count: number;
  gross_notional: number;
  net_notional: number;
  directional_imbalance: number;
  observed_coverage: number;
  verified_coverage: number;
  declared_coverage: number;
  unknown_coverage: number;
};

export type CohortRow = {
  cohort_id: string;
  cohort_predicate: string;
  windows: number;
  last_seen: string;
  peak_intents: number;
  peak_agents: number;
};

export type DependencyRow = {
  dependency_type: string;
  dependency_id: string;
  observations: number;
  verified: number;
  declared: number;
  unknown: number;
  agents: number;
  last_seen: string;
};

export type EvidenceEvent = {
  event_id: string;
  event_name: string;
  aggregate_id: string;
  correlation_id: string;
  causation_id?: string;
  occurred_at: string;
  producer: string;
  sequence: number;
  corrects_event_id?: string;
  payload?: Record<string, unknown>;
};

export const fleetState = () =>
  read<FleetMeasurement>(`${FLEET_ENGINE_URL}/v1/fleet/state`, "Fleet state");

export const cohorts = () => read<CohortRow>(`${FLEET_ENGINE_URL}/v1/cohorts`, "Cohorts");

export const dependencies = () =>
  read<DependencyRow>(`${FLEET_ENGINE_URL}/v1/dependencies`, "Dependencies");

export type ControlRow = {
  control_id: string;
  incident_id: string;
  action: string;
  cohort_id: string;
  agent_id: string;
  agent_ids?: string[];
  account_id: string;
  authorized_by: string;
  reason: string;
  applied_at: string;
  expires_at: string;
  in_force: boolean;
  revoked_at?: string;
  revoked_by?: string;
};

export type IncidentRow = {
  incident_id: string;
  correlation_id: string;
  severity: string;
  severity_rule: string;
  status: string;
  anomaly_rules: string[];
  shared_dependencies: string[];
  recommended: string;
  window_start: string;
  window_end: string;
  opened_at: string;
};

/** Authorized fleet controls, in force or not. Read-only, like everything here. */
export const controls = () =>
  read<ControlRow>(`${GATEWAY_URL}/v1/controls`, "Controls", "controls");

export const incidents = () =>
  read<IncidentRow>(`${FLEET_ENGINE_URL}/v1/incidents`, "Incidents", "incidents");

export type SimulationRow = {
  run_id: string;
  scenario: string;
  seed: number;
  requested_by: string;
  status: string;
  requested_at: string;
  completed_at?: string;
  experiment_id?: string;
  result_fingerprint?: string;
  scenario_source_hash?: string;
};

/** Digital Twin runs. The two hashes are what make a run arguable rather than trusted. */
export const simulations = () =>
  read<SimulationRow>(`${FLEET_ENGINE_URL}/v1/simulations`, "Simulations", "runs");

export type IntentRow = {
  envelope_id: string;
  correlation_id: string;
  received_at: string;
  last_event: string;
  last_at: string;
  events: string[];
  side?: string;
  instrument_id?: string;
  code?: string;
  action?: string;
  control?: string;
  broker_order_id?: string;
};

/** A tenant's recent intents, refusals included. Built from evidence, not from the
 *  idempotency table, which holds only what reached a venue. */
export const recentIntents = () =>
  read<IntentRow>(`${GATEWAY_URL}/v1/intents?limit=50`, "Intents", "intents");

export const evidenceFor = (correlationId: string) =>
  read<EvidenceEvent>(
    `${GATEWAY_URL}/v1/evidence?correlation_id=${encodeURIComponent(correlationId)}`,
    "Evidence",
  );
