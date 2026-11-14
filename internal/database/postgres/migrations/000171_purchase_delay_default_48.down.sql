-- 000171 down: restore the column default to 0. Rows moved from 0 to 48 are
-- not reverted: the up migration cannot record which rows it changed.
ALTER TABLE global_config ALTER COLUMN purchase_delay_hours SET DEFAULT 0;
