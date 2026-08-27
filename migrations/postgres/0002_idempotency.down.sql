BEGIN;
DROP POLICY IF EXISTS idempotency_tenant_isolation ON idempotency_records;
DROP TABLE IF EXISTS idempotency_records;
COMMIT;
