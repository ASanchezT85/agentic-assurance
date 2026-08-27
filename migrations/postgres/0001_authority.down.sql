-- Reverses 0001_authority.sql. The role is left in place: dropping a role that other
-- objects may depend on is a wider blast radius than this migration owns.

BEGIN;

DROP POLICY IF EXISTS authority_grants_tenant_isolation ON authority_grants;
DROP TABLE IF EXISTS authority_grants;

COMMIT;
