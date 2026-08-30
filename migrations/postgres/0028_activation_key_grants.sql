BEGIN;

-- What authorized each activation key.
--
-- Registering an activation key is the strongest act on this platform: an activation key
-- says which bundle enforces, and a bundle says what every agent in the tenant may not
-- do. Until now the only way to add one was a row written by hand, and the table recorded
-- the key without recording who granted it.
--
-- Two facts are kept here that policy_activation_keys cannot hold: the nonce of the
-- authorization, which is what makes a replay a database conflict rather than a second
-- registration, and the key that signed for it — empty exactly once per tenant, for the
-- first key, which nothing could have signed for.
CREATE TABLE IF NOT EXISTS policy_activation_key_grants (
    tenant_id        text        NOT NULL,
    nonce            text        NOT NULL,

    action           text        NOT NULL,
    subject_key_id   text        NOT NULL,
    actor            text        NOT NULL,

    -- NULL is the bootstrap: the tenant's first key, registered by a named operator
    -- credential because no key existed to sign for it.
    signed_by_key_id text,

    authorized_at    timestamptz NOT NULL,
    accepted_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, nonce),

    CONSTRAINT policy_activation_key_grants_action
        CHECK (action IN ('REGISTER_ACTIVATION_KEY', 'BOOTSTRAP_ACTIVATION_KEY'))
);

CREATE INDEX IF NOT EXISTS policy_activation_key_grants_subject_idx
    ON policy_activation_key_grants (tenant_id, subject_key_id);

ALTER TABLE policy_activation_key_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_activation_key_grants FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS policy_activation_key_grants_tenant_isolation
    ON policy_activation_key_grants;
CREATE POLICY policy_activation_key_grants_tenant_isolation ON policy_activation_key_grants
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- No UPDATE and no DELETE. A grant of authority is a record of a customer's act.
GRANT SELECT, INSERT ON policy_activation_key_grants TO assurance_app;

COMMIT;
