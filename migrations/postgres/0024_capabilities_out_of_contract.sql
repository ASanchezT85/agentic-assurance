-- Capabilities leave the executable authority contract (ADR-026).
--
-- margin_allowed and shorting_allowed were written on every grant and read by nothing.
-- Detecting margin use or a short needs position data the platform does not hold, so a
-- grant could carry shorting_allowed = false while the platform authorized a short. A
-- permission that denies nothing is worse than an absent one: a customer reads it as a
-- control and sizes their risk against it.
--
-- The columns stay. Dropping them would destroy what past grants recorded, and those
-- rows are the evidence of what a customer asked for even though the platform never
-- applied it. What changes is that nothing may write a new one: the CHECK holds them at
-- false, so an application that regains the column by accident fails loudly instead of
-- storing a permission nobody enforces.

-- Dropped first so a replay of the migration set does not fail on the constraint it
-- added last time.
ALTER TABLE authority_grants DROP CONSTRAINT IF EXISTS authority_grants_no_capabilities;
ALTER TABLE authority_grants
    ADD CONSTRAINT authority_grants_no_capabilities
    CHECK (margin_allowed = false AND shorting_allowed = false) NOT VALID;

COMMENT ON COLUMN authority_grants.margin_allowed IS
    'Historical. Not part of the V0 contract and enforced by nothing; see ADR-026.';
COMMENT ON COLUMN authority_grants.shorting_allowed IS
    'Historical. Not part of the V0 contract and enforced by nothing; see ADR-026.';
