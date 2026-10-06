-- 000104 down: drop the last accepted TOTP step.
ALTER TABLE users DROP COLUMN IF EXISTS mfa_last_totp_counter;
