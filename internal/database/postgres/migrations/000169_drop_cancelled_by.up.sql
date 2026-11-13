-- Migration 000169 (issue #1747): finish the canceled rename (#47) by draining
-- the legacy purchase_executions.cancelled_by actor column into canceled_by and
-- dropping it. 000089 added canceled_by, 000157 normalized the status rows, and
-- every read since then projected COALESCE(canceled_by, cancelled_by). Reads
-- now take canceled_by alone.
--
-- ROLLBACK RISK (owner-accepted 2026-11-13): a build older than #1253 still
-- writes cancelled_by, so its writes fail once this column is gone. Do not roll
-- the application back past #1253 after this migration has run.
--
-- The drain runs under an ACCESS EXCLUSIVE lock so no writer can commit a
-- cancelled_by-only value between the UPDATE and the DROP. updated_at is the
-- plan-health and retention clock, so its trigger is disabled around the drain
-- (as in 000157); DISABLE TRIGGER is transactional.

LOCK TABLE purchase_executions IN ACCESS EXCLUSIVE MODE;

ALTER TABLE purchase_executions DISABLE TRIGGER update_purchase_executions_updated_at;

UPDATE purchase_executions
SET canceled_by = cancelled_by
WHERE canceled_by IS NULL
  AND cancelled_by IS NOT NULL;

ALTER TABLE purchase_executions ENABLE TRIGGER update_purchase_executions_updated_at;

ALTER TABLE purchase_executions DROP COLUMN cancelled_by;
