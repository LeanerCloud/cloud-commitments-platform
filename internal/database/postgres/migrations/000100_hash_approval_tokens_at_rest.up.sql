-- Migration 000100: hash approval/revocation tokens at rest (issue #103).
--
-- WHY
-- purchase_executions.approval_token and ri_exchange_history.approval_token
-- stored the raw, directly-usable secret. handler_ri_exchange.go compared it
-- with a plain subtle.ConstantTimeCompare against the stored value;
-- validateRevokeToken (purchase side) SHA-256'd the STORED raw value and the
-- supplied value at COMPARE time, which only normalizes length for the
-- constant-time compare and does not mean the column is hashed. The
-- consuming endpoints (/api/purchases/approve, /api/purchases/cancel,
-- /api/purchases/revoke, /api/ri-exchange/approve, /api/ri-exchange/reject)
-- are unauthenticated and CSRF-exempt, so token possession alone commits or
-- reverses money. A read-only DB compromise (SQL injection, a leaked
-- backup/snapshot, a read replica, an over-permissioned analytics role)
-- therefore escalates straight to spending money.
--
-- WHAT
-- Rewrites every existing non-empty value in place to
-- encode(digest(approval_token, 'sha256'), 'hex') -- the same SHA-256 hex
-- digest format Go's config.HashApprovalToken (and internal/auth's
-- hashSessionToken, for session/reset/invite tokens) produces. No column
-- type change is needed: both columns are VARCHAR(255) and a SHA-256 hex
-- digest is 64 characters, well inside that width, so this is a plain
-- in-place UPDATE rather than an expand-contract (new column + backfill +
-- drop-old-column) migration.
--
-- Because the digest is deterministic, a still-outstanding token from an
-- already-sent email keeps validating after this migration: the application
-- layer (this PR's Go changes, deployed together with this migration) now
-- hashes the SUPPLIED token at compare time and compares it against the
-- stored value, so hash(supplied) == migrated_stored_hash continues to hold
-- for every unexpired token. Zero outstanding approval/revoke/reject links
-- are invalidated by this migration.
--
-- pgcrypto is required for digest(); it is not yet enabled in this database
-- (only uuid-ossp and pg_trgm are, per migration 000001).
CREATE EXTENSION IF NOT EXISTS pgcrypto;

UPDATE purchase_executions
   SET approval_token = encode(digest(approval_token, 'sha256'), 'hex')
 WHERE approval_token IS NOT NULL
   AND approval_token <> '';

UPDATE ri_exchange_history
   SET approval_token = encode(digest(approval_token, 'sha256'), 'hex')
 WHERE approval_token IS NOT NULL
   AND approval_token <> '';
