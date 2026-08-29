-- Authority limits become a reservation rather than a check followed by a record.
--
-- The check and the record were two transactions with a venue call between them:
-- authority read consumed usage, the pipeline submitted the order, and only afterwards
-- was usage written. Every operation was race-free and the invariant was not — four
-- concurrent intents of 4,000 against a 10,000 rolling ceiling put 16,000 through,
-- reproduced against this database before the column existed.
--
-- INV-002 is a property of the system, not of the ledger. A structure that never loses
-- a concurrent write is not a limit; a limit is a decision nobody else can make at the
-- same moment.

BEGIN;

ALTER TABLE authority_usage
    ADD COLUMN IF NOT EXISTS state text NOT NULL DEFAULT 'COMMITTED';

-- Existing rows are COMMITTED by construction: they were written after a submission
-- the venue had already accepted, which is exactly what that state means.
ALTER TABLE authority_usage
    DROP CONSTRAINT IF EXISTS authority_usage_state_valid;
ALTER TABLE authority_usage
    ADD CONSTRAINT authority_usage_state_valid
        CHECK (state IN ('RESERVED', 'COMMITTED', 'RELEASED'));

-- The reservation's own query: everything not released, for one grant, in a window.
-- Released rows are capacity returned — an order a venue definitively refused never
-- existed, and leaving it consumed would let anyone exhaust a customer's grant with
-- requests that were always going to be rejected.
CREATE INDEX IF NOT EXISTS authority_usage_reservation_idx
    ON authority_usage (tenant_id, grant_id, submitted_at DESC)
    WHERE state <> 'RELEASED';

COMMIT;
