# telemetry-sdk

Client-side helpers for emitting fleet telemetry to NATS JetStream.

Phase 0: boundary marker only. Implemented in Phase 8 alongside the ClickHouse
pipeline. Telemetry is asynchronous and never on the hard-policy path (§29, ADR-021).
