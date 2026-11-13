-- 000169 down: re-add the raw approval_token columns, empty.
--
-- Raw tokens cannot be restored (only their SHA-256 digests survive in
-- approval_token_hash), so the columns come back NULLABLE with no default and
-- no backfill: no value is invented. The ri_exchange_history UNIQUE constraint
-- is not recreated; the hash column keeps the uniqueness.
ALTER TABLE purchase_executions ADD COLUMN approval_token VARCHAR(255);
ALTER TABLE ri_exchange_history ADD COLUMN approval_token VARCHAR(255);
