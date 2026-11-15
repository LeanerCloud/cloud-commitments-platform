-- Reverse 000174: drop the durable purchase claims (cloud-commitments-go#745).
-- Lossy by design: live claims are dropped, so concurrent replicas fall back to the
-- provider flow's in-process lock until the table is recreated.
DROP TABLE IF EXISTS purchase_claims;
