BEGIN;

-- The application may return capacity for an order that was never sent.
--
-- PostgresUsage.Release is a DELETE, deliberately: a released row is capacity returned
-- either way, and removing it leaves the key genuinely free for a later, properly
-- evaluated request rather than leaving a stale row somebody could inherit.
--
-- assurance_app was never granted DELETE on this table, so every release since it existed
-- failed with "permission denied" and the capacity stayed held. A customer's rolling and
-- daily windows filled with reservations for orders that do not exist, and legitimate
-- orders were refused against a limit nobody had spent.
--
-- The platform had been recording the failure the whole time. The pipeline writes an
-- evidence event when Release returns an error, and the evidence store held 56 of them and
-- not one successful release, each saying "permission denied for table authority_usage".
--
-- DELETE only. No UPDATE beyond what the app already has, and the row is still protected by
-- row level security: this returns capacity, it does not let anything rewrite what was
-- spent.
GRANT DELETE ON authority_usage TO assurance_app;

COMMIT;
