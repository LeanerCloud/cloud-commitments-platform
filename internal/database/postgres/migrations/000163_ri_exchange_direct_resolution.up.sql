-- Operator resolution of a stuck AWS direct RI exchange row (issue #1836).
-- The original status and completed_at are never rewritten; the operator's
-- verdict is recorded beside them, and a resolved row stops holding its
-- source RIs (the guard adds `resolved_at IS NULL`).
ALTER TABLE ri_exchange_direct_executions
    ADD COLUMN IF NOT EXISTS resolved_status TEXT
        CONSTRAINT ri_exchange_direct_executions_resolved_status_check
        CHECK (resolved_status IN ('completed', 'failed')),
    ADD COLUMN IF NOT EXISTS resolved_by_user_id UUID,
    ADD COLUMN IF NOT EXISTS resolved_by TEXT,
    ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS resolution_note TEXT;

-- A resolution is whole or absent. resolved_by_user_id stays optional (a
-- break-glass SQL settle or an API key has no user id) but never exists
-- without a resolution.
ALTER TABLE ri_exchange_direct_executions
    DROP CONSTRAINT IF EXISTS ri_exchange_direct_executions_resolution_all_or_none;
ALTER TABLE ri_exchange_direct_executions
    ADD CONSTRAINT ri_exchange_direct_executions_resolution_all_or_none
    CHECK ((resolved_status IS NULL) = (resolved_by IS NULL)
       AND (resolved_status IS NULL) = (resolved_at IS NULL)
       AND (resolved_status IS NULL) = (resolution_note IS NULL)
       AND (resolved_by_user_id IS NULL OR resolved_status IS NOT NULL));
