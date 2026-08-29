BEGIN;
ALTER TABLE authority_usage ALTER COLUMN notional TYPE numeric(20, 2);
COMMIT;
