-- The outbox publisher gets its own role, and the application loses the cross-tenant
-- read it never needed.
--
-- evidence_outbox had one policy, FOR ALL USING (true), granted to assurance_app. The
-- reasoning was about the publisher: one process drains every tenant's queue, and a
-- per-tenant loop would drop a tenant the moment its credential rotated out. The
-- reasoning was sound and the grant was not. assurance_app is the role every request
-- handler connects as, so the exemption written for a background job applied to the
-- whole application, and any bug that read evidence_outbox inside a request read every
-- tenant's rows.
--
-- A capability granted to a role is granted to everything that connects as it. So the
-- publisher becomes a different role: it reads across tenants and can do nothing else,
-- and assurance_app goes back to seeing only the tenant its transaction has set.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'assurance_outbox') THEN
        CREATE ROLE assurance_outbox LOGIN PASSWORD 'assurance_outbox_dev_only'
            NOSUPERUSER NOBYPASSRLS;
    END IF;
END
$$;

GRANT CONNECT ON DATABASE assurance TO assurance_outbox;
GRANT USAGE ON SCHEMA public TO assurance_outbox;

DROP POLICY IF EXISTS evidence_outbox_tenant_isolation ON evidence_outbox;
-- Dropped so a replay of the migration set re-creates them rather than failing.
DROP POLICY IF EXISTS evidence_outbox_app_isolation ON evidence_outbox;
DROP POLICY IF EXISTS evidence_outbox_publisher ON evidence_outbox;

-- The application: its own tenant, in both directions. Same rule as every other table.
CREATE POLICY evidence_outbox_app_isolation ON evidence_outbox
    FOR ALL
    TO assurance_app
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- The publisher: every tenant's rows, because that is its whole job. It is narrow in a
-- different direction — SELECT and UPDATE only, below — so it can mark what it drained
-- and cannot create, delete or alter the content of an event. The rows it reads are
-- already-committed events on their way to a bus that carries the tenant in the subject;
-- it makes no decisions and returns nothing to a caller.
CREATE POLICY evidence_outbox_publisher ON evidence_outbox
    FOR ALL
    TO assurance_outbox
    USING (true)
    WITH CHECK (true);

REVOKE ALL ON evidence_outbox FROM assurance_outbox;
GRANT SELECT, UPDATE ON evidence_outbox TO assurance_outbox;

-- The publisher reads the event body to publish it, and never writes one.
GRANT SELECT ON evidence_events TO assurance_outbox;

-- evidence_events carries its own RLS, so a SELECT grant alone would return nothing.
DROP POLICY IF EXISTS evidence_events_publisher ON evidence_events;
CREATE POLICY evidence_events_publisher ON evidence_events
    FOR SELECT
    TO assurance_outbox
    USING (true);
