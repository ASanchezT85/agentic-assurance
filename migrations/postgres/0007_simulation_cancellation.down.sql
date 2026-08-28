BEGIN;
ALTER TABLE simulation_runs DROP CONSTRAINT IF EXISTS simulation_cancelled_has_an_actor;
ALTER TABLE simulation_runs DROP CONSTRAINT IF EXISTS simulation_status_valid;
ALTER TABLE simulation_runs ADD CONSTRAINT simulation_status_valid CHECK (
    status IN ('QUEUED', 'RUNNING', 'COMPLETED', 'FAILED')
);
ALTER TABLE simulation_runs DROP COLUMN IF EXISTS cancelled_at;
ALTER TABLE simulation_runs DROP COLUMN IF EXISTS cancelled_by;
COMMIT;
