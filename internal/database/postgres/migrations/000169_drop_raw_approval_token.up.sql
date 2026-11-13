-- Migration 000169: drop the raw approval_token columns (issue #422).
--
-- Contract step of the expand-contract started by 000100 (#416/#103). The
-- expand is already complete: every environment runs code that reads and
-- writes only approval_token_hash, so the raw columns are dead weight and a
-- leftover copy of the secret the approve/cancel/revoke/reject endpoints accept.
--
-- IRREVERSIBLE: the raw values were already replaced by their SHA-256 digests
-- (000100) and are discarded here. The down migration re-adds the columns
-- empty (NULL); it cannot restore any value.
--
-- First the 000100 sweep runs again, so a raw token that pre-#103 code wrote
-- after 000100 (prod migrates manually) is hashed before its column goes.
-- Each sweep keys on a non-empty raw value and never on the hash column, so a
-- row whose raw value is empty keeps its existing approval_token_hash. Unlike
-- 000100 there is no CASE that nulls the hash for an empty raw value.
--
-- Dropping approval_token also drops the ri_exchange_history UNIQUE constraint
-- from 000009 (and its backing index); uniqueness lives on
-- idx_ri_exchange_history_approval_token_hash from 000100.

UPDATE purchase_executions
   SET approval_token_hash = encode(sha256(convert_to(approval_token, 'UTF8')), 'hex'),
       approval_token = ''
 WHERE approval_token <> '';

UPDATE ri_exchange_history
   SET approval_token_hash = encode(sha256(convert_to(approval_token, 'UTF8')), 'hex'),
       approval_token = NULL
 WHERE approval_token IS NOT NULL AND approval_token <> '';

ALTER TABLE purchase_executions DROP COLUMN approval_token;
ALTER TABLE ri_exchange_history DROP COLUMN approval_token;
