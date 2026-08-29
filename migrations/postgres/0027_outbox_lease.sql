BEGIN;

-- The outbox gains a lease, so several publishers can drain it without doing the same
-- work twice.
--
-- Measured before this: evidence arrived at roughly 2,200 events per second under
-- sustained load and drained at 100 to 200, leaving a queue of 131,346 that took about
-- twenty minutes to clear. That is not transient queueing — the arrival rate exceeded
-- the service rate, and a queue in that relationship diverges. The analytical plane and
-- the fleet engine lagged a busy period by tens of minutes, which is exactly the period
-- an incident review looks at.
--
-- Three causes, all structural: one batch of 100 per one-second tick, one UPDATE
-- transaction per event to mark it published, and a SELECT with no claim, so two
-- publishers would select the same oldest rows and burn the capacity needed to catch up.
--
-- The lease fixes the third and makes the first two safe to fix: a publisher claims rows
-- with FOR UPDATE SKIP LOCKED, publishes them, and marks them in one statement.

ALTER TABLE evidence_outbox
    ADD COLUMN IF NOT EXISTS claimed_at timestamptz,
    ADD COLUMN IF NOT EXISTS claimed_by text;

-- The claim is a lease rather than a lock: a publisher that dies mid-batch must not
-- strand its rows. A claim older than the reclaim window is available again, and
-- re-publishing an event is harmless because delivery is at-least-once by design
-- (ADR-008) and the consumer deduplicates by event id.
CREATE INDEX IF NOT EXISTS evidence_outbox_claimable_idx
    ON evidence_outbox (created_at)
    WHERE published_at IS NULL;

DROP INDEX IF EXISTS evidence_outbox_unpublished_idx;

COMMENT ON COLUMN evidence_outbox.claimed_at IS
    'When a publisher took this row. A stale claim is reclaimed; see 0027.';
COMMENT ON COLUMN evidence_outbox.claimed_by IS
    'Which publisher instance holds the lease, for operators reading a stalled queue.';

COMMIT;
