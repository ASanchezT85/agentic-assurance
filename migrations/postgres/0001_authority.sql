-- Phase 3: authority grants.
--
-- These are the first tenant-scoped records the platform persists, which is why
-- INV-007 (tenant A cannot observe tenant B) is owned by this phase (ADR-024).
--
-- Reversible: see 0001_authority.down.sql.

BEGIN;

-- The application role.
--
-- This is load-bearing, not tidiness. POSTGRES_USER in the container is a superuser,
-- and PostgreSQL exempts superusers from row level security entirely. Connecting the
-- application as the superuser would make every policy below silently inert, and the
-- isolation tests would pass against a database enforcing nothing.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'assurance_app') THEN
        CREATE ROLE assurance_app LOGIN PASSWORD 'assurance_app_dev_only' NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS authority_grants (
    grant_id              text PRIMARY KEY,
    tenant_id             text        NOT NULL,
    principal_id          text        NOT NULL,
    account_id            text        NOT NULL,
    agent_id              text        NOT NULL,

    issued_at             timestamptz NOT NULL,
    valid_from            timestamptz NOT NULL,
    valid_until           timestamptz NOT NULL,

    allowed_operations    text[]      NOT NULL DEFAULT '{}',
    allowed_asset_classes text[]      NOT NULL DEFAULT '{}',
    allowed_instruments   text[]      NOT NULL DEFAULT '{}',
    denied_instruments    text[]      NOT NULL DEFAULT '{}',

    per_order_notional    numeric(20, 4) NOT NULL DEFAULT 0,
    rolling_1h_notional   numeric(20, 4) NOT NULL DEFAULT 0,
    daily_notional        numeric(20, 4) NOT NULL DEFAULT 0,
    max_open_orders       integer        NOT NULL DEFAULT 0,

    margin_allowed        boolean     NOT NULL DEFAULT false,
    shorting_allowed      boolean     NOT NULL DEFAULT false,

    status                text        NOT NULL,
    revoked_at            timestamptz,
    revocation_reason     text,

    CONSTRAINT authority_grants_status_valid CHECK (status IN ('ACTIVE', 'REVOKED')),
    -- A revoked grant without a revocation time cannot be explained later.
    CONSTRAINT authority_grants_revocation_complete CHECK (
        (status = 'REVOKED' AND revoked_at IS NOT NULL) OR
        (status = 'ACTIVE'  AND revoked_at IS NULL)
    ),
    CONSTRAINT authority_grants_window_ordered CHECK (valid_until > valid_from)
);

CREATE INDEX IF NOT EXISTS authority_grants_tenant_agent_idx
    ON authority_grants (tenant_id, agent_id);

-- Row level security.
--
-- FORCE matters as much as ENABLE: without it the table owner bypasses the policy,
-- so an isolation test run as the owner would pass while enforcing nothing.
ALTER TABLE authority_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE authority_grants FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS authority_grants_tenant_isolation ON authority_grants;
CREATE POLICY authority_grants_tenant_isolation ON authority_grants
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON authority_grants TO assurance_app;

COMMIT;
