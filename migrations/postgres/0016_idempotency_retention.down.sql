BEGIN;
REVOKE DELETE ON idempotency_records FROM assurance_app;
DROP INDEX IF EXISTS idempotency_retention_idx;
COMMIT;
