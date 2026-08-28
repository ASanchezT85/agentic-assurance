-- authority_grants was keyed by grant_id alone. Every other table in this schema is
-- keyed (tenant_id, ...), and this one was not.
--
-- Found by the end-to-end test, which creates a grant called grant_e2e for one tenant
-- per case. The second case's insert conflicted with the first case's row in a
-- different tenant, and the ON CONFLICT DO UPDATE then failed row level security for a
-- row it was never allowed to see.
--
-- Two consequences, and the second is the one that matters:
--
--  1. Grant ids are a global namespace, so two tenants cannot choose ids
--     independently. A customer's naming convention can collide with another
--     customer's.
--  2. The collision is observable. A tenant that inserts grant_id X and gets an
--     isolation error rather than a clean insert has learned that X exists in some
--     other tenant. Failing closed is correct; leaking through the failure is not
--     (INV-007).

BEGIN;

-- Guarded, because migration 0001 now creates the composite key directly and a
-- fresh database has nothing to change.
DO $$
BEGIN
    IF (SELECT count(*) FROM pg_index i
        JOIN pg_class c ON c.oid = i.indrelid
        WHERE c.relname = 'authority_grants' AND i.indisprimary
          AND array_length(i.indkey::int2[], 1) = 1) = 1
    THEN
        ALTER TABLE authority_grants DROP CONSTRAINT authority_grants_pkey;
        ALTER TABLE authority_grants ADD PRIMARY KEY (tenant_id, grant_id);
    END IF;
END $$;

COMMIT;
