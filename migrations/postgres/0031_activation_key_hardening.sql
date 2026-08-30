BEGIN;

-- The bootstrap, as a database invariant rather than a read-before-write.
--
-- "A tenant's first activation key is a bootstrap, possible exactly once" was implemented
-- by counting the tenant's active keys and taking the bootstrap path when the count was
-- zero. Between that read and the insert, a second request could read zero too, and both
-- committed: an operator credential could turn concurrency into two keys and keep more
-- policy authority than the design says the bootstrap ever grants.
--
-- One bootstrap grant per tenant, enforced here. It also settles the other half: a
-- bootstrap does not reopen because keys later expire or are revoked, since the grant that
-- records it is never deleted. Bootstrap is a one-time authority event, not a statement
-- about who can sign right now.
-- If this fails, a tenant already has two bootstrap grants and the defect has already
-- fired there. That is a refusal to proceed rather than something to clean up
-- automatically: which of the two keys should remain is a question about who holds policy
-- authority for that customer, and it is not one a migration may answer on its own.
CREATE UNIQUE INDEX IF NOT EXISTS policy_activation_key_grants_one_bootstrap
    ON policy_activation_key_grants (tenant_id)
    WHERE action = 'BOOTSTRAP_ACTIVATION_KEY';

COMMIT;
