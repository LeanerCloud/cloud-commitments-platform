-- Issue #1758: one default payment option, no-upfront. The column defaults
-- were all-upfront while the Go fallback, DefaultSettings and the UI disagreed.
-- The coordinator picked no-upfront (the safe, no-cash-outlay choice).
--
-- Column defaults change unconditionally. service_configs.payment changes the
-- default only (no row update): production inserts pass payment explicitly.
--
-- The seeded global_config row moves only when it is provably untouched:
-- id = 1, still all-upfront, and updated_at = created_at. The
-- update_global_config_updated_at trigger bumps updated_at on every UPDATE, so
-- any admin save leaves the row unequal. The one writer that skips the trigger
-- is the store's INSERT branch (SaveGlobalConfig when no row exists), which is
-- unreachable in production because the seeded row is never deleted; even
-- there the value guard leaves anything but all-upfront alone.
--
-- The trigger is disabled for the data UPDATE so the row stays recognisably
-- untouched (for the down migration and audits). This file runs as one implicit
-- transaction, so the trigger cannot be left disabled (see 000145).
ALTER TABLE global_config ALTER COLUMN default_payment SET DEFAULT 'no-upfront';
ALTER TABLE service_configs ALTER COLUMN payment SET DEFAULT 'no-upfront';

ALTER TABLE global_config DISABLE TRIGGER update_global_config_updated_at;

UPDATE global_config
SET default_payment = 'no-upfront'
WHERE id = 1
  AND default_payment = 'all-upfront'
  AND updated_at = created_at;

ALTER TABLE global_config ENABLE TRIGGER update_global_config_updated_at;
