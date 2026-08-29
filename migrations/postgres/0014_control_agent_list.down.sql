BEGIN;
ALTER TABLE fleet_controls DROP CONSTRAINT IF EXISTS fleet_controls_scope_is_one_kind;
ALTER TABLE fleet_controls DROP COLUMN IF EXISTS agent_ids;
COMMIT;
