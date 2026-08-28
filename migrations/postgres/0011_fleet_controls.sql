-- Fleet controls that a customer authorized.
--
-- internal/fleet has carried Recommendation, Authorization and Authorize since Phase
-- 13, and Authorize is the only function in the codebase that produces an enforceable
-- fleet control. Nothing called it outside its own tests: every recommendation the
-- platform ever made stopped at shadow mode, not by policy but because there was no
-- surface a customer could authorize one through.
--
-- PostgreSQL and not ClickHouse, and read on the hot path: a control is a decision the
-- enforcement plane must honour on the next order, and the analytical plane is
-- forbidden there (INV-005).

BEGIN;

CREATE TABLE IF NOT EXISTS fleet_controls (
    tenant_id        text        NOT NULL,
    control_id       text        NOT NULL,

    -- What the platform recommended, and where. A control that cannot name the
    -- recommendation it came from is an operator acting on a hunch six months ago.
    incident_id      text        NOT NULL,
    action           text        NOT NULL,

    -- The scope, concrete and local. Cohort membership is computed by the
    -- intelligence plane from a rolling window, and an enforcement check that had to
    -- ask the fleet engine who is in a cohort would fail closed every time the
    -- analytical plane blinked (INV-005). The cohort is resolved to agents and
    -- accounts at authorization time; NULL means every agent or account in the tenant.
    agent_id         text,
    account_id       text,
    cohort_id        text        NOT NULL,

    -- The customer's authorization, INV-009's half of the bargain, stored whole
    -- because "who let this happen" is the question an audit opens with.
    authorized_by    text        NOT NULL,
    policy_bundle_id text        NOT NULL,
    reason           text        NOT NULL,

    applied_at       timestamptz NOT NULL,

    -- Required. A control with no expiry is one nobody has to renew, and a platform
    -- that quietly throttles an agent forever because of an incident last spring is
    -- worse than one that stops throttling too early.
    expires_at       timestamptz NOT NULL,
    revoked_at       timestamptz,
    revoked_by       text,

    PRIMARY KEY (tenant_id, control_id),

    CONSTRAINT fleet_controls_action_valid
        CHECK (action IN ('THROTTLE', 'REQUIRE_APPROVAL', 'ISOLATE_COHORT', 'READ_ONLY')),
    CONSTRAINT fleet_controls_expiry_after_application CHECK (expires_at > applied_at),
    CONSTRAINT fleet_controls_revocation_has_an_author
        CHECK (revoked_at IS NULL OR revoked_by IS NOT NULL)
);

-- The hot path's query: everything still in force for this tenant. It runs on every
-- submission, so it is indexed rather than scanned.
CREATE INDEX IF NOT EXISTS fleet_controls_in_force_idx
    ON fleet_controls (tenant_id, expires_at) WHERE revoked_at IS NULL;

ALTER TABLE fleet_controls ENABLE ROW LEVEL SECURITY;
ALTER TABLE fleet_controls FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS fleet_controls_tenant_isolation ON fleet_controls;
CREATE POLICY fleet_controls_tenant_isolation ON fleet_controls
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON fleet_controls TO assurance_app;

COMMIT;
