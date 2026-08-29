-- One scale for money.
--
-- Grant limits were numeric(20,4) and consumed usage numeric(20,2), so the number a
-- ceiling was evaluated against was not necessarily the number later counted against
-- it: a reservation of 1,000.005 was authorized at four decimal places and stored at
-- two. A ceiling that is approximately enforced is not a ceiling.
--
-- Existing rows widen without loss — two decimal places fit inside four — and
-- everything written from here on arrives as decimal text from an exact type rather
-- than through a float.

BEGIN;

ALTER TABLE authority_usage
    ALTER COLUMN notional TYPE numeric(20, 4);

COMMIT;
