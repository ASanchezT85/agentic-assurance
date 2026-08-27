BEGIN;
DROP TRIGGER IF EXISTS evidence_no_update ON evidence_events;
DROP FUNCTION IF EXISTS evidence_is_append_only();
DROP POLICY IF EXISTS evidence_tenant_isolation ON evidence_events;
DROP TABLE IF EXISTS evidence_events;
COMMIT;
