-- Phase 6: append-only evidence.
--
-- ADR-009 and INV-006 say historical evidence cannot be silently mutated. The way
-- to mean that is not a code review convention, it is a privilege: the application
-- role gets INSERT and SELECT on this table and nothing else. A rogue UPDATE fails
-- in the database, whatever the calling code believes it is allowed to do.

BEGIN;

CREATE TABLE IF NOT EXISTS evidence_events (
    event_id          text        PRIMARY KEY,
    schema_version    text        NOT NULL,
    event_name        text        NOT NULL,
    tenant_id         text        NOT NULL,
    aggregate_id      text        NOT NULL,
    correlation_id    text        NOT NULL,
    causation_id      text,

    occurred_at       timestamptz NOT NULL,
    produced_at       timestamptz NOT NULL,
    recorded_at       timestamptz NOT NULL DEFAULT now(),
    producer          text        NOT NULL,
    sequence          bigint      NOT NULL,

    -- A correction references the event it supersedes. The superseded row is left
    -- exactly as it was recorded (ADR-009).
    corrects_event_id text,

    payload           jsonb       NOT NULL DEFAULT '{}'::jsonb,

    CONSTRAINT evidence_sequence_non_negative CHECK (sequence >= 0),
    CONSTRAINT evidence_correction_names_target CHECK (
        event_name <> 'evidence.corrected.v1' OR corrects_event_id IS NOT NULL
    ),
    CONSTRAINT evidence_no_self_correction CHECK (
        corrects_event_id IS NULL OR corrects_event_id <> event_id
    )
);

-- Timeline reconstruction is the point of the table, so the ordering it needs is
-- the index that exists.
CREATE INDEX IF NOT EXISTS evidence_correlation_idx
    ON evidence_events (tenant_id, correlation_id, occurred_at, sequence);

CREATE INDEX IF NOT EXISTS evidence_aggregate_idx
    ON evidence_events (tenant_id, aggregate_id, occurred_at);

ALTER TABLE evidence_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS evidence_tenant_isolation ON evidence_events;
CREATE POLICY evidence_tenant_isolation ON evidence_events
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- The load-bearing line of this migration. No UPDATE. No DELETE. Not "we agreed not
-- to": the privilege is absent, so the database refuses.
REVOKE ALL ON evidence_events FROM assurance_app;
GRANT SELECT, INSERT ON evidence_events TO assurance_app;

-- A trigger as well as a privilege, because a future migration that grants UPDATE by
-- accident would otherwise silently reopen the door.
CREATE OR REPLACE FUNCTION evidence_is_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'evidence_events is append-only (ADR-009, INV-006); '
                    'corrections are new rows referencing corrects_event_id';
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS evidence_no_update ON evidence_events;
CREATE TRIGGER evidence_no_update
    BEFORE UPDATE OR DELETE ON evidence_events
    FOR EACH ROW EXECUTE FUNCTION evidence_is_append_only();

COMMIT;
