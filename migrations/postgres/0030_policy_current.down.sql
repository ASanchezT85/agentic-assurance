BEGIN;

-- Dropping the pointer returns the platform to deciding what is current by comparing
-- timestamps two gateways generated from their own clocks, and removes the serialization
-- that keeps a tenant's policy history from branching. The history itself survives.
DROP TABLE IF EXISTS policy_current;
DROP INDEX IF EXISTS policy_activations_seq_idx;
ALTER TABLE policy_activations DROP COLUMN IF EXISTS transition_seq;

COMMIT;
