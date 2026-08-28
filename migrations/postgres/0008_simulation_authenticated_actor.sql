-- Who actually asked, as opposed to who said they did.
--
-- requested_by and cancelled_by are text the caller typed. They are worth keeping:
-- a credential identifies a service, not the person operating it, so the human name
-- has nowhere else to come from. But on their own they made the audit trail's answer
-- to "who stopped this run" whatever the requester chose to write.
--
-- The authenticated credential is recorded beside them. Not settable by the caller.

BEGIN;

ALTER TABLE simulation_runs
    ADD COLUMN IF NOT EXISTS submitted_by text,
    ADD COLUMN IF NOT EXISTS cancelled_by_identity text;

COMMIT;
