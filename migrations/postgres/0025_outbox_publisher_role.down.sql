-- Rolling back 0025 is a security downgrade, and it refuses to happen quietly.
--
-- The obvious down migration recreates what 0025 removed: one policy, FOR ALL
-- USING (true), granted to assurance_app — the cross-tenant read that landed on the role
-- every request handler connects as. Restoring the prior schema is not the same thing as
-- being harmless, and a rollback that silently reopens a known cross-tenant surface is
-- worse than no rollback, because it looks like maintenance.
--
-- So this drops the publisher's access, which is safe in any direction, and stops. The
-- application keeps its tenant-scoped policy, which is the correct rule regardless of
-- whether 0025 is applied; what is lost is the publisher's ability to drain, so evidence
-- stays committed in the outbox and no order is affected.
--
-- An operator who genuinely needs the old broad-read behaviour has to write it out by
-- hand, having read this. That is the point.

DROP POLICY IF EXISTS evidence_events_publisher ON evidence_events;
DROP POLICY IF EXISTS evidence_outbox_publisher ON evidence_outbox;

REVOKE ALL ON evidence_outbox FROM assurance_outbox;
REVOKE ALL ON evidence_events FROM assurance_outbox;

-- evidence_outbox_app_isolation is left in place deliberately. It is the tenant-scoped
-- rule every other table already has, and reverting to USING (true) would hand every
-- request handler a read across tenants.
--
-- To restore the pre-0025 behaviour anyway — knowing that it grants assurance_app a
-- cross-tenant read of evidence_outbox:
--
--   DROP POLICY IF EXISTS evidence_outbox_app_isolation ON evidence_outbox;
--   CREATE POLICY evidence_outbox_tenant_isolation ON evidence_outbox
--       FOR ALL
--       USING (true)
--       WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
