-- THROTTLE, the one control action with no enforcement path.
--
-- POST /v1/controls refused it rather than storing it, because a control the platform
-- records and does not apply is the shadow-mode confusion that endpoint exists to end.
-- Refusing was honest and it left spec section 16 three-quarters implemented: an
-- operator watching a cohort misbehave could isolate it or stop it dead, and could not
-- simply slow it down, which is the proportionate response and therefore the one they
-- would reach for most.
--
-- A rate limit needs a counter. This is it.

BEGIN;

-- The rate a THROTTLE control permits. Null for every other action, and required for
-- THROTTLE: a throttle with no rate is not a lenient throttle, it is an absent one.
ALTER TABLE fleet_controls
    ADD COLUMN IF NOT EXISTS max_orders     integer,
    ADD COLUMN IF NOT EXISTS window_seconds integer;

ALTER TABLE fleet_controls
    DROP CONSTRAINT IF EXISTS fleet_controls_throttle_has_a_rate;
ALTER TABLE fleet_controls
    ADD CONSTRAINT fleet_controls_throttle_has_a_rate CHECK (
        (action <> 'THROTTLE' AND max_orders IS NULL AND window_seconds IS NULL)
        OR (action = 'THROTTLE' AND max_orders > 0 AND window_seconds > 0)
    );

-- One row per order a throttled scope was allowed to send.
--
-- Keyed by the idempotency key as well, for the reason authority_usage is: a replayed
-- submission must not spend the window twice. A duplicate arrives, conflicts, and the
-- caller is allowed through on the slot it already holds.
CREATE TABLE IF NOT EXISTS fleet_control_usage (
    tenant_id       text        NOT NULL,
    control_id      text        NOT NULL,
    idempotency_key text        NOT NULL,
    submitted_at    timestamptz NOT NULL,

    PRIMARY KEY (tenant_id, control_id, idempotency_key)
);

-- The hot path's query: how many orders this control has let through since a moment.
CREATE INDEX IF NOT EXISTS fleet_control_usage_window_idx
    ON fleet_control_usage (tenant_id, control_id, submitted_at DESC);

ALTER TABLE fleet_control_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE fleet_control_usage FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS fleet_control_usage_tenant_isolation ON fleet_control_usage;
CREATE POLICY fleet_control_usage_tenant_isolation ON fleet_control_usage
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, DELETE ON fleet_control_usage TO assurance_app;

COMMIT;
