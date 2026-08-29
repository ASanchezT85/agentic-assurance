-- Which fleet control refused an order, in the analytical plane.
--
-- A control refusal already reaches the row correctly as unauthorized flow: the
-- authority decision is AUTHORITY_OK and the policy action is empty, and the hydrator
-- counts an intent as authorized only when both allow. The row is right and it is
-- silent about why.
--
-- That silence has a cost the fleet engine exists to avoid. An operator who authorized
-- a THROTTLE on a cohort and wants to know whether it did anything cannot ask: the
-- intents it stopped look exactly like intents a policy rule stopped. "Did the control
-- work" is the question that follows every control, and answering it from evidence
-- means reading one chain at a time.
--
-- The code, not a boolean. CONTROL_READ_ONLY, CONTROL_THROTTLED and
-- CONTROL_COHORT_ISOLATED are different operational stories, and a column that only
-- said "a control refused this" would have to be joined back to evidence to tell them
-- apart — which is the join this column exists to avoid.

ALTER TABLE assurance.intents
    ADD COLUMN IF NOT EXISTS control_decision LowCardinality(String) AFTER policy_action,
    ADD COLUMN IF NOT EXISTS control_id String AFTER control_decision;
