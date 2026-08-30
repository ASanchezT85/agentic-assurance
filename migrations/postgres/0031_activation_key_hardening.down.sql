BEGIN;

-- Dropping this returns the bootstrap to being a read followed by a write, which two
-- concurrent requests can both win.
DROP INDEX IF EXISTS policy_activation_key_grants_one_bootstrap;

COMMIT;
