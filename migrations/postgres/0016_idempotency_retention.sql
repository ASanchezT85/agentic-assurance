-- The application role could not delete an idempotency record.
--
-- Spec section 19 asks for bounded retention and 0002 granted SELECT, INSERT and
-- UPDATE — which was right until something needed to prune. The sweeper's first run
-- deleted nothing and said so in a warning nobody was reading yet.
--
-- DELETE and no more. The role still cannot drop or truncate the table, and the
-- sweeper's own rules are what keep a PENDING record safe: a permission is not a
-- policy.

BEGIN;

GRANT DELETE ON idempotency_records TO assurance_app;

-- The sweep's query: resolved records older than a cutoff. Without this it is a scan
-- of the whole table every hour, and the table is the one the hot path claims into.
CREATE INDEX IF NOT EXISTS idempotency_retention_idx
    ON idempotency_records (state, updated_at);

COMMIT;
