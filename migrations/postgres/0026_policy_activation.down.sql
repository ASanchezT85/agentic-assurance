-- Dropping these removes the record of who authorized which policy to enforce.
--
-- Safe only before any activation has been accepted. After that the rows are the
-- customer's own account of their control changes, and no other table holds them.
DROP TABLE IF EXISTS policy_activations;
DROP TABLE IF EXISTS policy_activation_keys;
