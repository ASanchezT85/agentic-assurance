-- Looking a submitted intent up by its envelope id.
--
-- GET /v1/intents/{id} is in the section 46 list and was never built: a caller could
-- submit an order and had no way to ask what happened to it, short of reading the
-- evidence chain. The outcome lives in idempotency_records, which is keyed by the
-- idempotency key; the envelope id was stored and never indexed.
--
-- Unique, and that is the second thing this migration does.
--
-- Spec section 12.2 requires envelope_id to identify one intent, and nothing enforced
-- it: two submissions carrying the same envelope id under different idempotency keys
-- produced two orders for what claims to be one intent. Found by trying to add this
-- index, which failed — on twenty-five rows a test suite had created by reusing a
-- fixed id without noticing. A caller could do the same by accident and get two fills.
--
-- The index is where it is enforced, because that is the only place a second writer
-- cannot race past.

BEGIN;

CREATE UNIQUE INDEX IF NOT EXISTS idempotency_envelope_idx
    ON idempotency_records (tenant_id, envelope_id);

COMMIT;
