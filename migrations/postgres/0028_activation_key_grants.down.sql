BEGIN;

-- Dropping this drops the record of who granted each activation key. The keys survive in
-- policy_activation_keys and keep working; what is lost is the answer to "who authorized
-- the key that authorized this policy", and the nonce registry that refuses a replayed
-- key authorization.
DROP TABLE IF EXISTS policy_activation_key_grants;

COMMIT;
