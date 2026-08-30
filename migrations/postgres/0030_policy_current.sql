BEGIN;

-- Which policy is in force, as one serialized chain per tenant.
--
-- policy_activations is the history and stays append-only. What it could not answer is
-- "which of these is current", and the code answered it with ORDER BY accepted_at over a
-- timestamp each gateway generated from its own clock. Two consequences, both of which let
-- a policy nobody authorized become the one that enforces:
--
--   * a signed authorization that was never presented stayed valid for ever. Sign B0→B1,
--     activate B0→B2 instead, and the old document still had an unused nonce and a good
--     signature — so B1 took effect and the customer's actual last decision was undone;
--
--   * two replicas could each accept a different transition from the same predecessor.
--     Both nonces were unique, so both inserted, and the tenant's history branched.
--
-- This table is the serialization point. A transition is accepted only while holding its
-- row, only when the predecessor the customer signed is the one actually in force, and the
-- order of the chain is transition_seq — assigned under that lock by the database, never
-- read from a clock.
CREATE TABLE IF NOT EXISTS policy_current (
    tenant_id           text        PRIMARY KEY,

    nonce               text        NOT NULL,
    bundle_id           text        NOT NULL,
    bundle_content_hash text        NOT NULL,

    -- The chain's order. Compared, never displayed: a customer reads dates, and a replica
    -- deciding what is current must not.
    transition_seq      bigint      NOT NULL,

    accepted_at         timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE policy_current ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_current FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS policy_current_tenant_isolation ON policy_current;
CREATE POLICY policy_current_tenant_isolation ON policy_current
    USING (tenant_id = current_setting('app.tenant_id', true))
    WITH CHECK (tenant_id = current_setting('app.tenant_id', true));

-- UPDATE, because this is a pointer rather than a record. What happened is in
-- policy_activations and nothing here can rewrite that.
GRANT SELECT, INSERT, UPDATE ON policy_current TO assurance_app;

-- The position of each transition in its tenant's chain.
ALTER TABLE policy_activations
    ADD COLUMN IF NOT EXISTS transition_seq bigint;

-- Backfill by the ordering the old code used, which is the best available reading of a
-- history recorded before there was an authoritative one. It is a starting point for the
-- counter, not a claim that the old ordering was right.
WITH ordered AS (
    SELECT tenant_id, nonce,
           row_number() OVER (PARTITION BY tenant_id ORDER BY accepted_at) AS seq
      FROM policy_activations
)
UPDATE policy_activations a
   SET transition_seq = ordered.seq
  FROM ordered
 WHERE a.tenant_id = ordered.tenant_id
   AND a.nonce = ordered.nonce
   AND a.transition_seq IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS policy_activations_seq_idx
    ON policy_activations (tenant_id, transition_seq);

-- And the pointer for tenants that already have a history.
INSERT INTO policy_current
    (tenant_id, nonce, bundle_id, bundle_content_hash, transition_seq, accepted_at)
SELECT DISTINCT ON (tenant_id)
       tenant_id, nonce, bundle_id, bundle_content_hash, transition_seq, accepted_at
  FROM policy_activations
 WHERE transition_seq IS NOT NULL
 ORDER BY tenant_id, transition_seq DESC
ON CONFLICT (tenant_id) DO NOTHING;

COMMIT;
