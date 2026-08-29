-- Reverting drops the lease. A single publisher still works; two would select the same
-- rows and waste the capacity a backlog needs to clear.
CREATE INDEX IF NOT EXISTS evidence_outbox_unpublished_idx
    ON evidence_outbox (created_at) WHERE published_at IS NULL;
DROP INDEX IF EXISTS evidence_outbox_claimable_idx;
ALTER TABLE evidence_outbox
    DROP COLUMN IF EXISTS claimed_at,
    DROP COLUMN IF EXISTS claimed_by;
