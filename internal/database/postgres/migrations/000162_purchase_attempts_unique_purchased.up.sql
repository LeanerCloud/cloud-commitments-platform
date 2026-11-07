-- Migration 000162: at most one purchased attempt per reservation (cloud-commitments-go#700).
--
-- A reservation ID recorded as purchased by one attempt can never be recorded
-- as purchased by another, whatever the token or execution. The second write
-- fails, which the provider reports as an unknown outcome instead of a
-- double-counted purchase. The pre-check names offending IDs so an operator can
-- decide which attempt row to keep before re-running the migration.
DO $$
DECLARE
    dups TEXT;
BEGIN
    SELECT string_agg(reservation_id, ', ' ORDER BY reservation_id) INTO dups
    FROM (
        SELECT reservation_id
        FROM purchase_attempts
        WHERE state = 'purchased'
        GROUP BY reservation_id
        HAVING count(*) > 1
    ) d;
    IF dups IS NOT NULL THEN
        RAISE EXCEPTION 'purchase_attempts holds duplicate purchased reservation_id values: %. Resolve the duplicate rows (keep the true buyer, delete or reclassify the rest) and re-run the migration.', dups;
    END IF;
END
$$;

CREATE UNIQUE INDEX IF NOT EXISTS uq_purchase_attempts_purchased_reservation
    ON purchase_attempts (reservation_id)
    WHERE state = 'purchased';
