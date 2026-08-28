-- A measurement must say how much of the flow the enforcement plane allowed through.
--
-- Found by running it. A live window reported 41 intents and a gross notional of
-- 78,105, and about half of that was refused before it reached a venue. The number
-- was not wrong: the fleet vector measures agentic INTENT, and forty agents wanting
-- to sell is the same signal whether or not they were allowed to. But nothing in the
-- row said which, and an operator reading gross notional would take it for flow that
-- hit a market.
--
-- ADR-014: a figure travels with what it was computed over.

ALTER TABLE assurance.fleet_measurements
    ADD COLUMN IF NOT EXISTS authorized_intents UInt64 AFTER intent_count,
    ADD COLUMN IF NOT EXISTS refused_intents UInt64 AFTER authorized_intents
