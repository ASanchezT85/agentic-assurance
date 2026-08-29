-- Retention is a customer's policy, not a number this platform picks.
--
-- The obvious design is one global period, and it is wrong: the platform is
-- infrastructure used by regulated institutions, and how long a record must exist
-- depends on the entity, the jurisdiction and the class of record. Encoding "seven
-- years" would be this system telling a customer what their obligation is.
--
-- So a tenant configures classes, and the platform enforces what it was told — with
-- two things it will not let a configuration override: an active legal hold, and
-- destruction that nobody authorized.

BEGIN;

CREATE TABLE IF NOT EXISTS retention_policies (
    tenant_id      text        NOT NULL,

    -- The class of record, not the table. A tenant may keep order assurance evidence
    -- for years and analytical telemetry for weeks, and both live in the same rows
    -- today: the class is what a policy is written against.
    record_class   text        NOT NULL,

    -- Days in PostgreSQL, then days in archive. Zero archive days means "keep
    -- indefinitely", which is the safe default rather than a value that expires.
    hot_days       integer     NOT NULL,
    archive_days   integer     NOT NULL DEFAULT 0,

    -- Where an exported month goes. Recorded rather than assumed, because "we archived
    -- it" without a destination is not an answer to a regulator.
    archive_destination text,

    -- NONE means a partition is archived and kept. AUTHORIZED means it may be
    -- destroyed, and only after an authorization row exists for it. There is
    -- deliberately no value meaning "delete automatically".
    deletion_mode  text        NOT NULL DEFAULT 'NONE',

    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     text        NOT NULL,

    PRIMARY KEY (tenant_id, record_class),

    CONSTRAINT retention_hot_days_positive CHECK (hot_days > 0),
    CONSTRAINT retention_archive_days_non_negative CHECK (archive_days >= 0),
    CONSTRAINT retention_deletion_mode CHECK (deletion_mode IN ('NONE', 'AUTHORIZED'))
);

-- A hold outranks every policy above.
--
-- Scoped to a tenant and optionally to one correlation id: an investigation is usually
-- about a chain rather than about a month, and a hold that could only pin whole months
-- would either over-hold or be ignored.
CREATE TABLE IF NOT EXISTS legal_holds (
    tenant_id      text        NOT NULL,
    hold_id        text        NOT NULL,
    correlation_id text,

    reason         text        NOT NULL,
    placed_by      text        NOT NULL,
    placed_at      timestamptz NOT NULL DEFAULT now(),

    released_at    timestamptz,
    released_by    text,

    PRIMARY KEY (tenant_id, hold_id),

    CONSTRAINT legal_hold_release_has_an_author
        CHECK (released_at IS NULL OR released_by IS NOT NULL)
);

CREATE INDEX IF NOT EXISTS legal_holds_active_idx
    ON legal_holds (tenant_id) WHERE released_at IS NULL;

-- What was exported, and how to prove it was not edited afterwards.
--
-- The hash chain is what makes an archived month still evidence. Each event's hash
-- covers the previous hash, so changing any row in an archive changes every hash after
-- it: an archive that verifies is one nobody rewrote, and an archive that does not
-- says where it stopped being true.
CREATE TABLE IF NOT EXISTS archive_manifests (
    tenant_id     text        NOT NULL,
    manifest_id   text        NOT NULL,

    partition     text        NOT NULL,
    period_start  timestamptz NOT NULL,
    period_end    timestamptz NOT NULL,

    event_count   bigint      NOT NULL,
    chain_head    text        NOT NULL,
    destination   text        NOT NULL,

    exported_at   timestamptz NOT NULL DEFAULT now(),
    exported_by   text        NOT NULL,

    verified_at   timestamptz,
    verified_by   text,

    PRIMARY KEY (tenant_id, manifest_id),

    CONSTRAINT archive_period_ordered CHECK (period_end > period_start),
    CONSTRAINT archive_event_count_non_negative CHECK (event_count >= 0)
);

-- Destruction, and who signed for it.
--
-- Two approvers rather than one. Deleting a regulated institution's records is the one
-- operation where a single mistaken command is unrecoverable, and the manifest of what
-- went is part of the authorization rather than a log line afterwards.
CREATE TABLE IF NOT EXISTS deletion_authorizations (
    tenant_id       text        NOT NULL,
    authorization_id text       NOT NULL,

    manifest_id     text        NOT NULL,
    reason          text        NOT NULL,

    requested_by    text        NOT NULL,
    requested_at    timestamptz NOT NULL DEFAULT now(),
    approved_by     text,
    approved_at     timestamptz,

    executed_at     timestamptz,
    events_deleted  bigint,

    PRIMARY KEY (tenant_id, authorization_id),

    -- An approval by the person who asked is not a second pair of eyes.
    CONSTRAINT deletion_two_people CHECK (approved_by IS NULL OR approved_by <> requested_by),
    CONSTRAINT deletion_approved_before_executed
        CHECK (executed_at IS NULL OR approved_at IS NOT NULL)
);

ALTER TABLE retention_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE retention_policies FORCE ROW LEVEL SECURITY;
ALTER TABLE legal_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE legal_holds FORCE ROW LEVEL SECURITY;
ALTER TABLE archive_manifests ENABLE ROW LEVEL SECURITY;
ALTER TABLE archive_manifests FORCE ROW LEVEL SECURITY;
ALTER TABLE deletion_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE deletion_authorizations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS retention_policies_tenant_isolation ON retention_policies;
CREATE POLICY retention_policies_tenant_isolation ON retention_policies
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP POLICY IF EXISTS legal_holds_tenant_isolation ON legal_holds;
CREATE POLICY legal_holds_tenant_isolation ON legal_holds
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP POLICY IF EXISTS archive_manifests_tenant_isolation ON archive_manifests;
CREATE POLICY archive_manifests_tenant_isolation ON archive_manifests
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

DROP POLICY IF EXISTS deletion_authorizations_tenant_isolation ON deletion_authorizations;
CREATE POLICY deletion_authorizations_tenant_isolation ON deletion_authorizations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON retention_policies TO assurance_app;
GRANT SELECT, INSERT, UPDATE ON legal_holds TO assurance_app;
GRANT SELECT, INSERT, UPDATE ON archive_manifests TO assurance_app;
GRANT SELECT, INSERT, UPDATE ON deletion_authorizations TO assurance_app;

COMMIT;
