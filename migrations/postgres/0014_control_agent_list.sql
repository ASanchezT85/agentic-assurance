-- A control can name several agents.
--
-- Four comments and the API reference said the cohort was "resolved to concrete agents
-- and accounts at authorization time". Nothing resolved anything: the handler stored
-- whichever single agent the caller typed, and the cohort predicate was carried along
-- as a label. The sentence was written as the intention and read afterwards as the
-- behaviour, which is the same failure this repository keeps finding in its own prose.
--
-- That made ISOLATE_COHORT an action whose name was a lie. A cohort is a predicate —
-- "model_family = claude AND strategy = momentum" — and the only scopes on offer were
-- one agent, one account, or the whole tenant. An operator answering a cohort incident
-- had to either stop the entire customer or authorize one control per agent and
-- remember which.
--
-- So a control may now carry a list. The platform still does not expand the predicate:
-- who is in a cohort is measured by the intelligence plane over a rolling window, and
-- an enforcement scope that changed as measurements arrived would be a control nobody
-- authorized. The operator names the members and the record says exactly whom it
-- bound.

BEGIN;

ALTER TABLE fleet_controls
    ADD COLUMN IF NOT EXISTS agent_ids text[];

ALTER TABLE fleet_controls
    DROP CONSTRAINT IF EXISTS fleet_controls_scope_is_one_kind;
ALTER TABLE fleet_controls
    ADD CONSTRAINT fleet_controls_scope_is_one_kind CHECK (
        -- At most one kind of scope. A control naming an agent and an account at once
        -- reads as either "both" or "either" depending on who is reading, and the two
        -- differ by an outage.
        (CASE WHEN agent_id IS NOT NULL THEN 1 ELSE 0 END)
      + (CASE WHEN account_id IS NOT NULL THEN 1 ELSE 0 END)
      + (CASE WHEN agent_ids IS NOT NULL THEN 1 ELSE 0 END) <= 1
        AND (agent_ids IS NULL OR array_length(agent_ids, 1) > 0)
    );

COMMIT;
