-- Listing a tenant's recent intents.
--
-- GET /v1/intents is in the section 46 list and was never built. The Flow console
-- surface asks for a correlation id instead, because there was nothing to list from:
-- the idempotency table holds only intents that reached a venue, so a list built from
-- it would show the accepted ones and silently omit every refusal — the half of the
-- record an assurance platform exists to keep.
--
-- The evidence store has all of them. What it lacked was a way to ask "the most recent
-- envelopes for this tenant": the existing indexes are keyed by aggregate and by
-- correlation id, so ordering a tenant's whole history by time was a scan.

BEGIN;

CREATE INDEX IF NOT EXISTS evidence_tenant_recent_idx
    ON evidence_events (tenant_id, occurred_at DESC);

COMMIT;
