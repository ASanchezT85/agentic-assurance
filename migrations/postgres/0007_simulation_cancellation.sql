-- Cancelling a run in flight.
--
-- A simulation is minutes of CPU on the customer's own infrastructure, and the only
-- way to stop one was to wait for the timeout. Worse, the slot it held was the scarce
-- thing: an operator who started the wrong scenario during an incident could not get
-- the capacity back to start the right one.
--
-- CANCELLED is terminal, and the guards on COMPLETED and FAILED are widened to say so.
-- An engine that finishes a moment after the cancellation lands must not resurrect the
-- run: the operator was told it was stopped, and a result appearing afterwards would
-- make that a lie.

BEGIN;

ALTER TABLE simulation_runs
    ADD COLUMN IF NOT EXISTS cancelled_at timestamptz,
    ADD COLUMN IF NOT EXISTS cancelled_by text;

ALTER TABLE simulation_runs DROP CONSTRAINT IF EXISTS simulation_status_valid;
ALTER TABLE simulation_runs ADD CONSTRAINT simulation_status_valid CHECK (
    status IN ('QUEUED', 'RUNNING', 'COMPLETED', 'FAILED', 'CANCELLED')
);

-- Who cancelled it, for the same reason the request records who asked for it: humans
-- are audited too (spec section 36), and "why did this run stop" is a question with an
-- answer.
ALTER TABLE simulation_runs DROP CONSTRAINT IF EXISTS simulation_cancelled_has_an_actor;
ALTER TABLE simulation_runs ADD CONSTRAINT simulation_cancelled_has_an_actor CHECK (
    status <> 'CANCELLED' OR (cancelled_at IS NOT NULL AND cancelled_by IS NOT NULL)
);

COMMIT;
