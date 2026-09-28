-- Migration 000100: hash approval/revocation tokens at rest (issue #103).
--
-- purchase_executions.approval_token (approval AND post-execution revocation
-- tokens) and ri_exchange_history.approval_token stored the raw secret that
-- the unauthenticated, CSRF-exempt approve/cancel/revoke/reject endpoints
-- accept, so a read-only DB compromise escalated to spending money.
--
-- Expand-contract, step 1 of 2: add approval_token_hash, move every raw
-- value into it as its SHA-256 hex digest (the format config.HashApprovalToken
-- produces), and NULL the raw column. The application reads and writes only
-- approval_token_hash from this release on. Dropping approval_token is a
-- later migration (follow-up issue), after the sweep below has been re-run.
--
-- The sweep keys on "raw IS NOT NULL", never on the hash column, so it is
-- idempotent and safe to re-run by hand: it only ever hashes a raw value once
-- (the raw is NULLed in the same statement) and it picks up rows that
-- pre-#103 code wrote after an earlier run (prod migrates manually, so old
-- code can run against the migrated schema for a while). A re-run can never
-- double-hash, unlike an in-place rewrite of approval_token.
--
-- sha256() is built in (PostgreSQL 11+), so no pgcrypto is needed. Empty
-- raw values mean "no token" and become NULL, not the digest of ''.

ALTER TABLE purchase_executions ADD COLUMN IF NOT EXISTS approval_token_hash VARCHAR(64);

UPDATE purchase_executions
   SET approval_token_hash = CASE WHEN approval_token <> ''
                                  THEN encode(sha256(convert_to(approval_token, 'UTF8')), 'hex')
                             END,
       approval_token = NULL
 WHERE approval_token IS NOT NULL;

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
