BEGIN;
DROP INDEX IF EXISTS authority_usage_reservation_idx;
ALTER TABLE authority_usage DROP CONSTRAINT IF EXISTS authority_usage_state_valid;
ALTER TABLE authority_usage DROP COLUMN IF EXISTS state;
COMMIT;
