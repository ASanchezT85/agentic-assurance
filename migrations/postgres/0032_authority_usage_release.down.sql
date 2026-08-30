BEGIN;

-- Revoking this returns the platform to holding capacity for orders that were never sent.
-- The failure is visible — the pipeline records it as evidence — and silent in its effect
-- until a customer's window is full.
REVOKE DELETE ON authority_usage FROM assurance_app;

COMMIT;
