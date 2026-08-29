-- Agent signing keys, so `agent_id` stops being caller-supplied data.
--
-- Transport identity establishes the tenant: a credential is issued to one, and a
-- workload SVID maps to one. The agent was a claim in the body. The platform knew
-- which customer was calling and took the envelope's word for which agent it was —
-- while the authority grant is scoped to exactly that agent, and section 12.2's
-- "invalid signature -> DENY" had nothing behind it.
--
-- A key is registered to one tenant and one agent. A key registered to tenant-A /
-- agent-7 must never verify an envelope claiming tenant-A / agent-8, and that is the
-- primary key rather than a check somebody remembers to write.
--
-- This proves control of a signing key. It does not prove which model produced the
-- reasoning: inferring that from a key is the inference ADR-006 and INV-014 forbid.

BEGIN;

CREATE TABLE IF NOT EXISTS agent_signing_keys (
    tenant_id   text        NOT NULL,
    agent_id    text        NOT NULL,
    key_id      text        NOT NULL,

    algorithm   text        NOT NULL,
    public_key  bytea       NOT NULL,

    status      text        NOT NULL DEFAULT 'ACTIVE',
    valid_from  timestamptz NOT NULL,
    valid_until timestamptz,
    revoked_at  timestamptz,
    revoked_by  text,
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (tenant_id, agent_id, key_id),

    -- One algorithm in V0. An algorithm field the caller controls is a downgrade
    -- attack unless the platform decides which values are acceptable, and one value is
    -- the smallest way to decide.
    CONSTRAINT agent_signing_keys_algorithm CHECK (algorithm = 'Ed25519'),
    CONSTRAINT agent_signing_keys_status CHECK (status IN ('ACTIVE', 'REVOKED')),
    CONSTRAINT agent_signing_keys_ed25519_size CHECK (octet_length(public_key) = 32),
    CONSTRAINT agent_signing_keys_revocation_has_an_author
        CHECK (revoked_at IS NULL OR revoked_by IS NOT NULL)
);

ALTER TABLE agent_signing_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_signing_keys FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS agent_signing_keys_tenant_isolation ON agent_signing_keys;
CREATE POLICY agent_signing_keys_tenant_isolation ON agent_signing_keys
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON agent_signing_keys TO assurance_app;

COMMIT;
