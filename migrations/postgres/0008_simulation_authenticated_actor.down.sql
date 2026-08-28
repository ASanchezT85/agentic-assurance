BEGIN;
ALTER TABLE simulation_runs
    DROP COLUMN IF EXISTS submitted_by,
    DROP COLUMN IF EXISTS cancelled_by_identity;
COMMIT;
