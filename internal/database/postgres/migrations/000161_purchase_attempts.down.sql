-- Reverse 000161: drop the purchase attempt journal (cloud-commitments-go#107).
-- Lossy by design: recorded attempts are dropped, so a re-dispatch falls back
-- to the provider's own window dedupe.
DROP TABLE IF EXISTS purchase_attempts;
