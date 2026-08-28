-- Incidents.
--
-- internal/incident has carried Detect, Open and Timeline.Reconstruct since Phase 10,
-- with tests, and nothing ever stored one or served one. Spec section 46 lists
-- GET /v1/incidents and GET /v1/incidents/{id}; section 49 requires the timeline to be
-- reproducible. It is reproducible in Go, from evidence, by nobody — because no
-- process opened an incident and no surface returned it.
--
-- PostgreSQL rather than ClickHouse: an incident's status changes, humans act on it,
-- and what an operator reads during an incident has to be authoritative (ADR-021).

BEGIN;

CREATE TABLE IF NOT EXISTS incidents (
    tenant_id           text        NOT NULL,
    incident_id         text        NOT NULL,

    -- The chain this incident belongs to, so the evidence and the incident are one
    -- investigation rather than two.
    correlation_id      text        NOT NULL,

    cohort_id           text        NOT NULL,
    window_start        timestamptz NOT NULL,
    window_end          timestamptz NOT NULL,

    severity            text        NOT NULL,
    status              text        NOT NULL,

    -- The anomalies and the shared dependencies, stored whole. "These agents all read
    -- the same feed" is the finding, and summarising it at write time would decide
    -- today which part of it a later question needs.
    anomalies           jsonb       NOT NULL,
    shared_dependencies jsonb       NOT NULL,

    -- What the platform suggested, and which rule assigned the severity. A reader who
    -- disagrees should be able to disagree with the rule rather than with the label.
    recommended         text        NOT NULL,
    severity_rule       text        NOT NULL,

    -- What people did. A recommendation is never an action (INV-009), so these are
    -- separate columns and not one field that blurs them.
    human_actions       jsonb       NOT NULL DEFAULT '[]'::jsonb,

    opened_at           timestamptz NOT NULL,
    closed_at           timestamptz,

    PRIMARY KEY (tenant_id, incident_id),

    CONSTRAINT incidents_severity_valid CHECK (severity IN ('LOW', 'MEDIUM', 'HIGH', 'CRITICAL')),
    CONSTRAINT incidents_status_valid CHECK (status IN ('OPEN', 'ACKNOWLEDGED', 'CLOSED')),
    CONSTRAINT incidents_closed_has_a_time CHECK (status <> 'CLOSED' OR closed_at IS NOT NULL),
    CONSTRAINT incidents_window_ordered CHECK (window_end > window_start)
);

-- One incident per cohort per window. The detector runs on every measurement, and
-- without this a cohort that stayed anomalous for ten windows would open ten
-- incidents for one situation — which is how an operator learns to ignore the list.
CREATE UNIQUE INDEX IF NOT EXISTS incidents_cohort_window_idx
    ON incidents (tenant_id, cohort_id, window_start);

CREATE INDEX IF NOT EXISTS incidents_recent_idx
    ON incidents (tenant_id, opened_at DESC);

ALTER TABLE incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE incidents FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS incidents_tenant_isolation ON incidents;
CREATE POLICY incidents_tenant_isolation ON incidents
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON incidents TO assurance_app;

COMMIT;
