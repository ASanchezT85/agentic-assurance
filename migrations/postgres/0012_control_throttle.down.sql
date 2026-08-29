BEGIN;
DROP TABLE IF EXISTS fleet_control_usage;
ALTER TABLE fleet_controls DROP CONSTRAINT IF EXISTS fleet_controls_throttle_has_a_rate;
ALTER TABLE fleet_controls DROP COLUMN IF EXISTS max_orders;
ALTER TABLE fleet_controls DROP COLUMN IF EXISTS window_seconds;
COMMIT;
