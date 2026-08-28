-- Simulation runs.
--
-- Spec section 46 lists POST /v1/simulations and GET /v1/simulations/{id}, and the
-- engine wrote its record to stdout: a run existed for as long as a terminal did.
--
-- PostgreSQL rather than ClickHouse because a run's status changes. ClickHouse is the
-- analytical store and is forbidden from anything that has to be read back
-- authoritatively (ADR-021, INV-011); the record a customer retrieves is exactly that.

BEGIN;

CREATE TABLE IF NOT EXISTS simulation_runs (
    tenant_id            text        NOT NULL,
    run_id               text        NOT NULL,

    scenario             text        NOT NULL,
    seed                 bigint      NOT NULL,

    -- Humans are audited too (spec section 36). A simulation runs against a
    -- customer's own configuration, and who asked is part of the record.
    requested_by         text        NOT NULL,

    status               text        NOT NULL,

    requested_at         timestamptz NOT NULL,
    started_at           timestamptz,
    completed_at         timestamptz,

    -- What makes the run reproducible, as columns as well as inside the record, so a
    -- run is findable by its fingerprint and not only by the id we assigned.
    experiment_id        text,
    result_fingerprint   text,
    scenario_source_hash text,

    -- The engine's output, whole. Summarising it at write time would mean deciding
    -- today which fields a future question needs.
    record               jsonb,
    error                text,

    PRIMARY KEY (tenant_id, run_id),

    CONSTRAINT simulation_status_valid CHECK (
        status IN ('QUEUED', 'RUNNING', 'COMPLETED', 'FAILED')
    ),
    -- A completed run without a fingerprint cannot be compared to another run, which
    -- is the only thing a simulation result is for.
    CONSTRAINT simulation_completed_is_reproducible CHECK (
        status <> 'COMPLETED' OR (result_fingerprint IS NOT NULL AND record IS NOT NULL)
    ),
    CONSTRAINT simulation_failed_says_why CHECK (
        status <> 'FAILED' OR error IS NOT NULL
    )
);

-- Listing a tenant's runs is newest-first.
CREATE INDEX IF NOT EXISTS simulation_runs_recent_idx
    ON simulation_runs (tenant_id, requested_at DESC);

-- Two runs of the same scenario with the same seed must produce the same fingerprint.
-- Finding out that they did not is the point of keeping it queryable.
CREATE INDEX IF NOT EXISTS simulation_runs_reproducibility_idx
    ON simulation_runs (tenant_id, scenario, seed);

ALTER TABLE simulation_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE simulation_runs FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS simulation_runs_tenant_isolation ON simulation_runs;
CREATE POLICY simulation_runs_tenant_isolation ON simulation_runs
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON simulation_runs TO assurance_app;

COMMIT;
