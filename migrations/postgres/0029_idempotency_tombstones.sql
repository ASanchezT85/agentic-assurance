BEGIN;

-- What the platform remembers about a request after its outcome is gone.
--
-- ADR-027 says an idempotency key identifies one economic request, permanently. Until now
-- only half of that was enforced. Authority refused a key whose identity differed — a
-- different envelope, grant, principal or amount — and allowed the identical request
-- through as a retry, which is correct while the execution record exists and is what makes
-- a retry keep the capacity it already holds.
--
-- After retention pruned the resolved record the two halves disagreed. Authority said "the
-- same request, it may proceed"; the execution store had no record, so nothing replayed and
-- nothing refused; and the venue was called a second time for one economic request. That is
-- INV-004 stated exactly, and it appeared thirty days after the fact, which is the worst
-- possible moment to discover it.
--
-- authority_usage cannot be the universal tombstone. A quantity-sized market order has no
-- notional the platform can determine before the venue prices it, so for those the row that
-- would remember the key is the row that does not exist; and envelope uniqueness is an
-- execution concern (§12.2) that authority has no reason to hold.
CREATE TABLE IF NOT EXISTS idempotency_tombstones (
    tenant_id       text        NOT NULL,
    idempotency_key text        NOT NULL,

    -- The envelope id, kept because pruning idempotency_records deletes the unique index
    -- that made an envelope identify one intent. Without this, §12.2 held only as long as
    -- retention had not run.
    envelope_id     text        NOT NULL,
    client_order_id text        NOT NULL,

    -- What the request ended as, so an operator asking "what happened to this key" gets a
    -- class of answer rather than silence. Not the outcome: the outcome was deliberately
    -- pruned and this table does not reopen it.
    final_state     text        NOT NULL,

    retired_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, idempotency_key),

    -- The two identities, both permanent.
    CONSTRAINT idempotency_tombstones_envelope UNIQUE (tenant_id, envelope_id)
);

ALTER TABLE idempotency_tombstones ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_tombstones FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS idempotency_tombstones_tenant_isolation ON idempotency_tombstones;
CREATE POLICY idempotency_tombstones_tenant_isolation ON idempotency_tombstones
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- No UPDATE and no DELETE. A tombstone that can be removed is a key that can be reopened,
-- which is the thing this table exists to make impossible.
GRANT SELECT, INSERT ON idempotency_tombstones TO assurance_app;

COMMIT;
