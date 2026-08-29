DROP POLICY IF EXISTS evidence_events_publisher ON evidence_events;
DROP POLICY IF EXISTS evidence_outbox_publisher ON evidence_outbox;
DROP POLICY IF EXISTS evidence_outbox_app_isolation ON evidence_outbox;
REVOKE ALL ON evidence_outbox FROM assurance_outbox;
REVOKE ALL ON evidence_events FROM assurance_outbox;

CREATE POLICY evidence_outbox_tenant_isolation ON evidence_outbox
    FOR ALL
    USING (true)
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));
