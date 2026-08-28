BEGIN;
ALTER TABLE authority_grants DROP CONSTRAINT authority_grants_pkey;
ALTER TABLE authority_grants ADD PRIMARY KEY (grant_id);
COMMIT;
