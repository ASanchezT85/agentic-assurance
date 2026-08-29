-- The outbox that puts NATS on the real path.
--
-- The repository implements EnsureStream, Publisher, Consumer and their tests, and no
-- binary ever constructed them: evidence went straight to PostgreSQL and fleet
-- telemetry straight to ClickHouse, while the documentation called JetStream the event
-- backbone. That is this project's own recurring defect — a component whose tests pass
-- while the running producer never calls it — found by an outside audit reading the
-- source rather than the docs.
--
-- The outbox is what makes publication safe rather than best-effort. An event and its
-- outbox row are written in the same transaction as the decision they describe, so a
-- committed event cannot be silently lost before publication, and a publisher that
-- crashes mid-flight retries from the table rather than from memory.
--
-- NATS stays off the critical path. Nothing here is read to authorize an order; the
-- gateway commits and returns, and publication happens afterwards (INV-005).

BEGIN;

CREATE TABLE IF NOT EXISTS evidence_outbox (
    outbox_id     bigserial   PRIMARY KEY,
    tenant_id     text        NOT NULL,
    event_id      text        NOT NULL,
    subject       text        NOT NULL,
    payload       jsonb       NOT NULL,

    created_at    timestamptz NOT NULL DEFAULT now(),
    published_at  timestamptz,
    attempt_count integer     NOT NULL DEFAULT 0,
    last_error    text,

    -- One row per event. At-least-once delivery is the contract downstream (ADR-008),
    -- and duplicating rows here would turn one event into two before it even left.
    CONSTRAINT evidence_outbox_event_unique UNIQUE (event_id)
);

-- The publisher's query: what has not gone out yet, oldest first.
CREATE INDEX IF NOT EXISTS evidence_outbox_unpublished_idx
    ON evidence_outbox (created_at) WHERE published_at IS NULL;

ALTER TABLE evidence_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE evidence_outbox FORCE ROW LEVEL SECURITY;

-- Two policies rather than one. Writes are tenant-scoped like everything else: the
-- gateway inserts inside a transaction that has already set app.tenant_id, and a write
-- for another tenant must fail.
--
-- Reads are not, and that is deliberate and narrow. The publisher is one process
-- draining every tenant's queue, and giving it a per-tenant loop would mean either
-- enumerating tenants from the credential registry — which drops a tenant the moment
-- its credential is rotated out — or a sweep that reads nothing. The rows it reads are
-- already-committed events on their way to a bus that carries the tenant in the
-- subject; it makes no decisions and returns nothing to a caller.
DROP POLICY IF EXISTS evidence_outbox_tenant_isolation ON evidence_outbox;
CREATE POLICY evidence_outbox_tenant_isolation ON evidence_outbox
    FOR ALL
    USING (true)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE, DELETE ON evidence_outbox TO assurance_app;
GRANT USAGE, SELECT ON SEQUENCE evidence_outbox_outbox_id_seq TO assurance_app;

COMMIT;
