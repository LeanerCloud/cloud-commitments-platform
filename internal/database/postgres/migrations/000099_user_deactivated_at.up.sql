-- Migration 000099: add users.deactivated_at (issue #89 / audit finding
-- A03-006).
--
-- Active alone cannot tell an admin-deactivated account from an invited
-- account that never completed setup (both are false), so a password reset
-- used to reactivate deactivated accounts. deactivated_at is set by
-- UpdateUser on deactivation and cleared on reactivation.
ALTER TABLE users ADD COLUMN deactivated_at TIMESTAMPTZ;

-- Backfill: before this change the API could not deactivate users, so any
-- existing inactive row was deactivated out of band or is an unfinished
-- invite. Login requires active = true, so a row with a login history is the
-- former. updated_at is only an approximation of when it happened; the gate
-- checks only NULL vs non-NULL.
UPDATE users
   SET deactivated_at = updated_at
 WHERE active = false
   AND last_login_at IS NOT NULL;
