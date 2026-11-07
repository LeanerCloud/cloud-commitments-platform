-- Reverse 000163: drop the operator-resolution columns (issue #1836).
-- Lossy by design: resolutions are dropped, so resolved rows that are still
-- submitted or unknown block their source RIs again, the fail-safe direction.
ALTER TABLE ri_exchange_direct_executions
    DROP CONSTRAINT IF EXISTS ri_exchange_direct_executions_resolution_all_or_none,
    DROP COLUMN IF EXISTS resolved_status,
    DROP COLUMN IF EXISTS resolved_by_user_id,
    DROP COLUMN IF EXISTS resolved_by,
    DROP COLUMN IF EXISTS resolved_at,
    DROP COLUMN IF EXISTS resolution_note;
