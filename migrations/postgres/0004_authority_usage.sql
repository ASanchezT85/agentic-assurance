-- Consumed usage per authority grant.
--
-- internal/authority/evaluate.go has named a PostgreSQL-backed UsageSource as the
-- hot path's implementation since Phase 3, and it did not exist. Nothing noticed
-- because nothing ran the path: passing nil makes every grant with a rolling limit
-- deny with USAGE_UNAVAILABLE, which is the correct failure and a useless system.
--
-- Not derived from idempotency_records, which carry neither a grant nor a notional.
-- They answer "did we already submit this", not "how much has this grant spent".

BEGIN;

CREATE TABLE IF NOT EXISTS authority_usage (
    tenant_id        text        NOT NULL,
    grant_id         text        NOT NULL,

    -- The idempotency key, so a replayed submission cannot spend a grant twice.
    idempotency_key  text        NOT NULL,

    notional         numeric(20, 2) NOT NULL,
    submitted_at     timestamptz NOT NULL,

    -- Open counts exposure still standing at a venue. MaxOpenOrders caps that, and
    -- a filled order is no longer it.
    open             boolean     NOT NULL DEFAULT true,
    closed_at        timestamptz,

    PRIMARY KEY (tenant_id, idempotency_key),

    CONSTRAINT authority_usage_notional_nonneg CHECK (notional >= 0),
    CONSTRAINT authority_usage_closed_is_not_open CHECK (open OR closed_at IS NOT NULL)
);

-- Every read is (tenant, grant) filtered by time. Without this the rolling window
-- scans the tenant's whole history on the hot path.
CREATE INDEX IF NOT EXISTS authority_usage_grant_window_idx
    ON authority_usage (tenant_id, grant_id, submitted_at DESC);

ALTER TABLE authority_usage ENABLE ROW LEVEL SECURITY;
ALTER TABLE authority_usage FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS authority_usage_tenant_isolation ON authority_usage;
CREATE POLICY authority_usage_tenant_isolation ON authority_usage
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON authority_usage TO assurance_app;

COMMIT;
