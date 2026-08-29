BEGIN;

-- Policy activation becomes a customer-authorized, durable act.
--
-- The bundle's rules were signed and their promotion into production was not: anyone who
-- could edit the policy file could take a correctly signed SHADOW bundle, change one word
-- to ACTIVE, and put it into force without the customer's key. The signature still
-- verified, because it covered the rules and the rules had not changed.
--
-- Two tables. One holds the keys that may authorize an activation; the other holds the
-- transitions that were accepted.

-- The keys that may authorize an activation.
--
-- Deliberately not the agent signing key registry. A policy activation is an operator's
-- or a customer's authority — a person approving what the platform will refuse — and an
-- autonomous trading agent is neither. Reusing one registry would mean any agent key
-- could promote a policy, which inverts INV-009: intelligence recommends and the customer
-- authorizes.
CREATE TABLE IF NOT EXISTS policy_activation_keys (
    tenant_id   text        NOT NULL,
    key_id      text        NOT NULL,

    algorithm   text        NOT NULL DEFAULT 'Ed25519',
    public_key  bytea       NOT NULL,

    -- Who the key belongs to, for the same reason a revocation needs an author: a key
    -- that authorized a policy change six months ago must still be attributable.
    holder      text        NOT NULL,

    status      text        NOT NULL DEFAULT 'ACTIVE',
    valid_from  timestamptz NOT NULL DEFAULT now(),
    valid_until timestamptz,

    revoked_at  timestamptz,
    revoked_by  text,

    PRIMARY KEY (tenant_id, key_id),

    CONSTRAINT policy_activation_keys_algorithm CHECK (algorithm = 'Ed25519'),
    CONSTRAINT policy_activation_keys_ed25519_size CHECK (octet_length(public_key) = 32),
    CONSTRAINT policy_activation_keys_status CHECK (status IN ('ACTIVE', 'REVOKED')),
    CONSTRAINT policy_activation_keys_revocation_has_an_author
        CHECK (revoked_at IS NULL OR revoked_by IS NOT NULL)
);

ALTER TABLE policy_activation_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_activation_keys FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS policy_activation_keys_tenant_isolation ON policy_activation_keys;
CREATE POLICY policy_activation_keys_tenant_isolation ON policy_activation_keys
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

GRANT SELECT, INSERT, UPDATE ON policy_activation_keys TO assurance_app;

-- The transitions that were accepted.
--
-- Durable, because the alternative was a process-local map: which bundle had been in
-- force was remembered only by the process that had witnessed it, so a restart recorded
-- the bundle it read at startup as a fresh activation the customer never performed, and
-- two replicas could disagree about the history of the same tenant.
--
-- A transition is written in the same transaction as its evidence. Enforcement changes
-- only after that transaction commits, so there is no state where the platform is
-- enforcing a policy whose authorization was never recorded.
CREATE TABLE IF NOT EXISTS policy_activations (
    tenant_id           text        NOT NULL,
    nonce               text        NOT NULL,

    bundle_id           text        NOT NULL,
    bundle_content_hash text        NOT NULL,

    prior_bundle_id           text,
    prior_bundle_content_hash text,

    action              text        NOT NULL,
    actor               text        NOT NULL,
    reason              text,
    key_id              text        NOT NULL,

    authorized_at       timestamptz NOT NULL,
    accepted_at         timestamptz NOT NULL DEFAULT now(),

    -- The nonce is the primary key, which is what makes a replay a conflict rather than
    -- a second activation. Presenting a captured authorization again is refused by the
    -- database, deterministically, on every replica.
    PRIMARY KEY (tenant_id, nonce),

    CONSTRAINT policy_activations_action CHECK (action IN ('ACTIVATE', 'ROLLBACK'))
);

-- What is in force now, and what came before it, read by accepted_at.
CREATE INDEX IF NOT EXISTS policy_activations_current_idx
    ON policy_activations (tenant_id, accepted_at DESC);

ALTER TABLE policy_activations ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_activations FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS policy_activations_tenant_isolation ON policy_activations;
CREATE POLICY policy_activations_tenant_isolation ON policy_activations
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- No UPDATE and no DELETE. An accepted transition is a record of a customer's act.
GRANT SELECT, INSERT ON policy_activations TO assurance_app;

COMMIT;
