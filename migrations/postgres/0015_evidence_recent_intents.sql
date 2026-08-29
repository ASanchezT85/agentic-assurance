-- Listing a tenant's recent intents without reading its whole day.
--
-- Measured, not suspected. Against 917,000 events the listing took 450–880 ms and the
-- plan says why: it grouped 909,061 rows into 177,087 aggregates and then kept fifty.
-- The query asked "which envelopes were active recently" by summarising every event of
-- every envelope in the window, which is a day of work to fill one page.
--
-- The newest intents are a bounded index scan if the index knows about the event name,
-- so this is the index for it. 0013's (tenant_id, occurred_at DESC) stays: it is what
-- bounds the window scan for everything else.

BEGIN;

CREATE INDEX IF NOT EXISTS evidence_tenant_received_idx
    ON evidence_events (tenant_id, event_name, occurred_at DESC);

COMMIT;
