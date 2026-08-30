BEGIN;

-- Dropping this reopens every key whose record retention has already pruned: the platform
-- forgets that those requests were ever made, and the next caller presenting one of those
-- keys reaches the venue again. Reversible in the schema sense only.
DROP TABLE IF EXISTS idempotency_tombstones;

COMMIT;
