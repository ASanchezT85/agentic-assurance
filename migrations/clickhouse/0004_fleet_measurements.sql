CREATE TABLE IF NOT EXISTS assurance.fleet_measurements
(
    tenant_id             LowCardinality(String),
    cohort_id             String,
    cohort_predicate      String,
    window_start          DateTime64(3, 'UTC'),
    window_end            DateTime64(3, 'UTC'),

    intent_count          UInt64,
    agent_count           UInt64,
    gross_notional        Float64,
    net_notional          Float64,
    directional_imbalance Float64,

    observed_coverage     Float64,
    verified_coverage     Float64,
    declared_coverage     Float64,
    unknown_coverage      Float64,

    computed_at           DateTime64(3, 'UTC') DEFAULT now64(3)
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(window_start)
ORDER BY (tenant_id, cohort_id, window_start)
