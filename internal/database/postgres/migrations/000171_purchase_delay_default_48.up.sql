-- Migration 000171: purchase_delay_hours defaults to 48 (issue #1934).
--
-- 000001 seeds the global_config row before 000070 adds purchase_delay_hours
-- with DEFAULT 0, so that ADD COLUMN backfilled the seeded row to 0 while the
-- docs and DefaultPurchaseDelayHours say 48. SET DEFAULT alone only affects
-- rows inserted later, so the row still at 0 is moved to 48 as well.
--
-- Existing installs that chose 0 on purpose (immediate execution) are changed
-- by this: the data cannot tell a deliberate 0 from the untouched seed. They
-- must set 0 again after the migration. A deployment without the
-- fire_scheduled_purchases sweep must keep 0 (approvals above 0 return 409).
-- Rows at any other value are left alone. The updated_at trigger is disabled
-- around the UPDATE so the seed row stays recognisable by updated_at =
-- created_at, which 000166's down migration relies on.

ALTER TABLE global_config ALTER COLUMN purchase_delay_hours SET DEFAULT 48;

ALTER TABLE global_config DISABLE TRIGGER update_global_config_updated_at;
UPDATE global_config SET purchase_delay_hours = 48 WHERE purchase_delay_hours = 0;
ALTER TABLE global_config ENABLE TRIGGER update_global_config_updated_at;
