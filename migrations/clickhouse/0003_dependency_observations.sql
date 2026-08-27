CREATE TABLE IF NOT EXISTS assurance.dependency_observations
(
    tenant_id       LowCardinality(String),
    envelope_id     String,
    agent_id        String,
    dependency_type LowCardinality(String),
    dependency_id   String,
    verification    LowCardinality(String),
    observed_at     DateTime64(3, 'UTC'),
    ingested_at     DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(observed_at)
ORDER BY (tenant_id, dependency_type, dependency_id, observed_at)
