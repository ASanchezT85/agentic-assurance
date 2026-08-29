-- A reservation says what it was reserved for.
--
-- The ledger was keyed by (tenant_id, idempotency_key) and held nothing else about the
-- request: not the envelope, not the principal, not the account. Reserve() saw a row
-- for the key and returned ALLOW without asking what it had been reserved for.
--
-- Three ways that becomes an authority bypass, all of them found by an audit reading
-- the code rather than by a crash:
--
--   a reservation left behind by a failure that never reached a venue can authorize a
--   different envelope with a different notional;
--
--   the same key under a different grant inherits the first grant's capacity;
--
--   an idempotency record pruned by retention leaves a usage row that makes a fresh
--   request invisible to rolling accounting — a deterministic long-term bypass rather
--   than a crash edge case.
--
-- The identity columns are what make a repeated key answerable: a retry matches on all
-- of them, and anything else is a different intent wearing the same key.

BEGIN;

ALTER TABLE authority_usage
    ADD COLUMN IF NOT EXISTS envelope_id  text,
    ADD COLUMN IF NOT EXISTS principal_id text,
    ADD COLUMN IF NOT EXISTS account_id   text;

-- Existing rows predate the columns and cannot claim an identity they never had. They
-- are marked rather than backfilled with a guess: a row whose envelope is unknown must
-- not silently match a request that names one.
UPDATE authority_usage SET envelope_id = '' WHERE envelope_id IS NULL;

COMMIT;
