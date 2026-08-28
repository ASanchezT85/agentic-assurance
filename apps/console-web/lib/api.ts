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

/** The tenant a reader is looking at. A header, and not authentication: the
 * authenticated-tenant requirement of section 46 arrives with the API surface that
 * carries authentication, and pretending otherwise here would be worse than saying
 * so. */
export const TENANT_ID = process.env.CONSOLE_TENANT_ID ?? "tenant_acme";

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

async function read<T>(url: string, what: string): Promise<Result<T>> {
  try {
    const response = await fetch(url, {
      headers: { "X-Tenant-Id": TENANT_ID },
      // Always fresh. A cached fleet view is a fleet view of the past presented as
      // the present.
      cache: "no-store",
    });

    if (response.status === 503) {
      return { available: false, reason: `${what} is unavailable: the store behind it is not reachable` };
    }
    if (!response.ok) {
      return { available: false, reason: `${what} returned ${response.status}` };
    }

    const body = (await response.json()) as { rows?: T[]; count?: number };
    return { available: true, rows: body.rows ?? [], count: body.count ?? 0 };
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

export const evidenceFor = (correlationId: string) =>
  read<EvidenceEvent>(
    `${GATEWAY_URL}/v1/evidence?correlation_id=${encodeURIComponent(correlationId)}`,
    "Evidence",
  );
