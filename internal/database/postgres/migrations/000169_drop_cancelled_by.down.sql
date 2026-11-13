-- Rollback 000169: re-add the legacy cancelled_by column and backfill it from
-- canceled_by so a build that still reads it sees the actors. The trigger is
-- disabled so updated_at (plan-health and retention clock) is not restamped.

LOCK TABLE purchase_executions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE purchase_executions ADD COLUMN IF NOT EXISTS cancelled_by TEXT;

ALTER TABLE purchase_executions DISABLE TRIGGER update_purchase_executions_updated_at;

UPDATE purchase_executions
SET cancelled_by = canceled_by
WHERE cancelled_by IS NULL
  AND canceled_by IS NOT NULL;

ALTER TABLE purchase_executions ENABLE TRIGGER update_purchase_executions_updated_at;
