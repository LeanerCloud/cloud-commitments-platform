-- 000099 down: drop users.deactivated_at.
ALTER TABLE users DROP COLUMN IF EXISTS deactivated_at;
