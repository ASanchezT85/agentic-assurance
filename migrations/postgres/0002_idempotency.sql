-- Phase 5: idempotency records.
--
-- ADR-015 resolved a contradiction in the master spec: section 17 requires a
-- duplicate to return the prior outcome, section 33.3 put the idempotency cache in
-- Redis, and INV-011 forbids Redis loss from destroying authoritative control state.
-- The prior outcome of an executable intent IS authoritative control state, so it
-- lives here. Redis holds a copy and is never asked to decide.

BEGIN;

CREATE TABLE IF NOT EXISTS idempotency_records (
    tenant_id        text        NOT NULL,
    idempotency_key  text        NOT NULL,
    envelope_id      text        NOT NULL,
    client_order_id  text        NOT NULL,

    state            text        NOT NULL,

    -- Outcome columns are null while PENDING. A record that claims to be RESOLVED
    -- without a state cannot answer the question the table exists to answer.
    outcome_state       text,
    broker_order_id     text,
    filled_quantity     numeric(20, 8),
    reject_reason       text,

    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL,

    PRIMARY KEY (tenant_id, idempotency_key),

    CONSTRAINT idempotency_state_valid CHECK (state IN ('PENDING', 'RESOLVED')),
    CONSTRAINT idempotency_resolved_has_outcome CHECK (
        state = 'PENDING' OR outcome_state IS NOT NULL
    )
);

-- Reconciliation looks an order up by the identifier the venue echoes back, so that
-- lookup has to be fast and unambiguous.
CREATE UNIQUE INDEX IF NOT EXISTS idempotency_client_order_id_idx
    ON idempotency_records (tenant_id, client_order_id);

ALTER TABLE idempotency_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_records FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS idempotency_tenant_isolation ON idempotency_records;
CREATE POLICY idempotency_tenant_isolation ON idempotency_records
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON idempotency_records TO assurance_app;

COMMIT;
