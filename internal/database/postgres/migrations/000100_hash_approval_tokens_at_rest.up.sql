-- Migration 000100: hash approval/revocation tokens at rest (issue #103).
--
-- purchase_executions.approval_token (approval AND post-execution revocation
-- tokens) and ri_exchange_history.approval_token stored the raw secret that
-- the unauthenticated, CSRF-exempt approve/cancel/revoke/reject endpoints
-- accept, so a read-only DB compromise escalated to spending money.
--
-- Expand-contract, step 1 of 2: add approval_token_hash, move every raw
-- value into it as its SHA-256 hex digest (the format config.HashApprovalToken
-- produces), and blank the raw column. The application reads and writes only
-- approval_token_hash from this release on. Dropping approval_token is a
-- later migration (follow-up issue), after the sweep below has been re-run.
--
-- Deploy order: run this migration BEFORE deploying the #103 code (that code
-- reads and writes approval_token_hash and fails without it), then re-run it
-- after the deploy to sweep raw tokens that pre-#103 code wrote in between.
-- Until that re-run those links fail closed as invalid, which is acceptable.
--
-- Re-running is safe because each sweep keys on a non-empty raw value, never
-- on the hash column, and blanks the raw in the same statement: a raw value is
-- hashed exactly once, a migrated row is never double-hashed, and a row that
-- pre-#103 code re-saved with an empty raw keeps its hash.
--
-- purchase_executions: the raw column is set to '' (not NULL) because pre-#103
-- code scans it into a Go string and would fail on NULL.
-- ri_exchange_history: the raw column is UNIQUE (000009), so it must become
-- NULL, not ''; pre-#103 code scans it into sql.NullString and writes '' as
-- NULL, so a non-NULL raw there is always a real token.
--
-- sha256() is built in (PostgreSQL 11+), so no pgcrypto is needed.

ALTER TABLE purchase_executions ADD COLUMN IF NOT EXISTS approval_token_hash VARCHAR(64);

-- #103 code no longer writes the raw column. Without a default its rows get a
-- NULL raw, which pre-#103 code (rollback, or an old revision still serving
-- during a rolling deploy) fails to scan into a Go string, breaking
-- GetExecutionByID and the whole GetExecutionsByStatuses list.
ALTER TABLE purchase_executions ALTER COLUMN approval_token SET DEFAULT '';

UPDATE purchase_executions
   SET approval_token_hash = encode(sha256(convert_to(approval_token, 'UTF8')), 'hex'),
       approval_token = ''
 WHERE approval_token <> '';

ALTER TABLE ri_exchange_history ADD COLUMN IF NOT EXISTS approval_token_hash VARCHAR(64);

UPDATE ri_exchange_history
   SET approval_token_hash = CASE WHEN approval_token <> ''
                                  THEN encode(sha256(convert_to(approval_token, 'UTF8')), 'hex')
                             END,
       approval_token = NULL
 WHERE approval_token IS NOT NULL;

-- GetRIExchangeRecordByToken looks rows up by hash; keep the uniqueness the
-- raw column had (000009).
CREATE UNIQUE INDEX IF NOT EXISTS idx_ri_exchange_history_approval_token_hash
    ON ri_exchange_history (approval_token_hash);
