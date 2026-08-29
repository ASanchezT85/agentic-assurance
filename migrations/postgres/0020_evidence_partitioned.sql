-- Evidence, partitioned by month.
--
-- The table grows with every decision the platform makes and nothing ever leaves it:
-- 916,000 events and 683 MB of test traffic alone, on a table the enforcement path
-- writes to on every order. Retention is not a delete statement — deleting years of
-- rows one at a time from a live table is how an archival policy becomes an outage —
-- and a partition can be detached in constant time.
--
-- This migration rebuilds the table. Doing it now, before any customer's evidence
-- exists, is the cheap moment; doing it later is a maintenance window.
--
-- What does not change: the table is append-only (the trigger comes with it), row level
-- security is per tenant, and PostgreSQL remains the record. What changes is that a
-- month can be archived and detached rather than scanned and deleted.
--
-- The primary key gains occurred_at, because PostgreSQL requires the partition key in
-- every unique constraint. Event id stays unique within a partition, which is what
-- deduplication needs: an event carries the moment it happened, and two events with the
-- same id in different months would be a clock that moved a year backwards.

-- Repeatable. scripts/migrate.sh replays every file, and this one rebuilds a table: a
-- second run renamed the partitioned table aside, found every partition name already
-- taken, and left a parent with no partitions. It failed inside its transaction and
-- rolled back, which is the only reason that was a scare rather than an outage.
SELECT (relkind = 'p') AS evidence_already_partitioned
  FROM pg_class WHERE relname = 'evidence_events' \gset

\if :evidence_already_partitioned
\echo 'evidence_events is already partitioned; skipping'
\else

BEGIN;

ALTER TABLE evidence_events RENAME TO evidence_events_unpartitioned;
ALTER INDEX IF EXISTS evidence_correlation_idx RENAME TO evidence_correlation_idx_old;
ALTER INDEX IF EXISTS evidence_aggregate_idx RENAME TO evidence_aggregate_idx_old;
ALTER INDEX IF EXISTS evidence_tenant_recent_idx RENAME TO evidence_tenant_recent_idx_old;
ALTER INDEX IF EXISTS evidence_tenant_received_idx RENAME TO evidence_tenant_received_idx_old;

CREATE TABLE evidence_events (
    event_id          text        NOT NULL,
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
    corrects_event_id text,
    payload           jsonb       NOT NULL DEFAULT '{}'::jsonb,

    PRIMARY KEY (event_id, occurred_at),

    CONSTRAINT evidence_sequence_non_negative CHECK (sequence >= 0),
    CONSTRAINT evidence_no_self_correction
        CHECK (corrects_event_id IS NULL OR corrects_event_id <> event_id),
    CONSTRAINT evidence_correction_names_target
        CHECK (event_name <> 'evidence.corrected.v1' OR corrects_event_id IS NOT NULL)
) PARTITION BY RANGE (occurred_at);

CREATE INDEX evidence_correlation_idx
    ON evidence_events (tenant_id, correlation_id, occurred_at, sequence);
CREATE INDEX evidence_aggregate_idx
    ON evidence_events (tenant_id, aggregate_id, occurred_at);
CREATE INDEX evidence_tenant_recent_idx
    ON evidence_events (tenant_id, occurred_at DESC);
CREATE INDEX evidence_tenant_received_idx
    ON evidence_events (tenant_id, event_name, occurred_at DESC);

ALTER TABLE evidence_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS evidence_tenant_isolation ON evidence_events;
CREATE POLICY evidence_tenant_isolation ON evidence_events
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT ON evidence_events TO assurance_app;

-- Append-only, still. ADR-009 and INV-006: a correction is a new event that references
-- the earlier one, and the earlier one stays exactly as it was recorded.
CREATE TRIGGER evidence_no_update
    BEFORE UPDATE OR DELETE ON evidence_events
    FOR EACH ROW EXECUTE FUNCTION evidence_is_append_only();

-- The partition the platform is writing into, plus its neighbours. Creating months
-- ahead of time rather than on demand: an insert that has to create its own partition
-- is an insert that can fail on the hot path for a reason nobody expects.
DO $$
DECLARE
    month date := date_trunc('month', now() - interval '2 months')::date;
    stop  date := date_trunc('month', now() + interval '6 months')::date;
    name  text;
BEGIN
    WHILE month <= stop LOOP
        name := 'evidence_events_' || to_char(month, 'YYYY_MM');
        EXECUTE format(
            'CREATE TABLE IF NOT EXISTS %I PARTITION OF evidence_events
             FOR VALUES FROM (%L) TO (%L)',
            name, month, (month + interval '1 month')::date);
        month := (month + interval '1 month')::date;
    END LOOP;
END $$;

-- Anything older or newer than the partitions above still has to land somewhere. A
-- default partition is what stops an insert failing because a month has not been
-- created yet; the planner reports rows sitting in it, because evidence in the default
-- partition cannot be archived by month.
CREATE TABLE IF NOT EXISTS evidence_events_default PARTITION OF evidence_events DEFAULT;

INSERT INTO evidence_events
    (event_id, schema_version, event_name, tenant_id, aggregate_id, correlation_id,
     causation_id, occurred_at, produced_at, recorded_at, producer, sequence,
     corrects_event_id, payload)
SELECT event_id, schema_version, event_name, tenant_id, aggregate_id, correlation_id,
       causation_id, occurred_at, produced_at, recorded_at, producer, sequence,
       corrects_event_id, payload
  FROM evidence_events_unpartitioned;

DROP TABLE evidence_events_unpartitioned;

COMMIT;

\endif
