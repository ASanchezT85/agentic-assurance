BEGIN;
-- Rebuilding back into one table is a maintenance operation, not a rollback
-- somebody runs by accident. Written out rather than automated.
SELECT 1;
COMMIT;
