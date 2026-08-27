CREATE TABLE IF NOT EXISTS assurance.intents
(
    tenant_id             LowCardinality(String),
    envelope_id           String,
    correlation_id        String,
    principal_id          String,
    account_id            String,
    agent_id              String,
    instrument_id         String,
    asset_class           LowCardinality(String),
    side                  LowCardinality(String),
    order_type            LowCardinality(String),

    notional              Nullable(Float64),
    quantity              Nullable(Float64),
    notional_determinable UInt8,

    strategy_id           String,
    authority_grant_id    String,
    attestation_level     LowCardinality(String),

    authority_decision    LowCardinality(String),
    policy_action         LowCardinality(String),
    policy_bundle_id      String,

    received_at           DateTime64(3, 'UTC'),
    ingested_at           DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(received_at)
ORDER BY (tenant_id, instrument_id, received_at, envelope_id)
