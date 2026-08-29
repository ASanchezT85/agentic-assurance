-- The analytical projection of the evidence stream.
--
-- PostgreSQL holds the evidence and always will: it is the record, it is append-only,
-- and a decision is reconstructed from it. This is the copy that answers questions
-- spanning months without scanning the enforcement plane's own table.
--
-- ReplacingMergeTree keyed on the event id, because delivery is at-least-once (ADR-008)
-- and a redelivered event must overwrite itself rather than count twice. A consumer
-- that could not tolerate a duplicate would be the defect, not the delivery.

CREATE TABLE IF NOT EXISTS assurance.evidence_stream
(
    tenant_id      LowCardinality(String),
    event_id       String,
    event_name     LowCardinality(String),
    aggregate_id   String,
    correlation_id String,
    causation_id   String,
    producer       LowCardinality(String),
    sequence       Int64,

    occurred_at    DateTime64(3, 'UTC'),
    projected_at   DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = ReplacingMergeTree(projected_at)
PARTITION BY toYYYYMM(occurred_at)
ORDER BY (tenant_id, event_id);
