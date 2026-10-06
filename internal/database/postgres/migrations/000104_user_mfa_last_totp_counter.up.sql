-- Migration 000104: remember the last accepted TOTP step per user (issue #442).
-- A code is accepted only for a step above this value, so an observed code
-- cannot be spent again inside its validity window (RFC 6238 section 5.2).
ALTER TABLE users ADD COLUMN mfa_last_totp_counter BIGINT NOT NULL DEFAULT 0;
