-- Flag pre-fix Azure purchase_history amounts as legacy estimates (issue #669).
--
-- Before the Azure repricing fix, upfront_cost and estimated_savings were
-- estimated over a window instead of read from the order. Those amounts must
-- stay out of dollar totals but the rows stay listed. amount_basis marks them:
-- NULL is a normal row, 'legacy_window' is a flagged one. Stage 2 widens the
-- CHECK; no other value is reserved here.
--
-- The ADD COLUMN takes an ACCESS EXCLUSIVE lock that is held for the UPDATE
-- below (milliseconds: one row per commitment). purchase_history has no
-- updated_at trigger, and the 000153 trigger fires only on listing columns.
--
-- A row is flagged when provider is Azure and either:
--  (a) a post-fix signature row exists (monthly payment, upfront_cost = 0,
--      monthly_cost > 0) and the row predates GREATEST(earliest signature row,
--      2026-10-08 09:15:51+00). The clamp is commit f2d60df8 (stale-pricing
--      refusal plus migration 106): every Azure execution after that deploy is
--      repriced;
--  (b) a monthly-family payment carries an upfront_cost > 0, at any time;
--  (c) no signature row exists: every Azure row is flagged, because prod ran
--      pre-fix code until its own deploy.
-- The predicate between the markers is shared with the runbook query in
-- runbooks/azure-legacy-amount-basis.md; a test keeps the two identical.
-- To undo a wrong boundary see that runbook (manual un-flag SQL).

ALTER TABLE purchase_history
    ADD COLUMN IF NOT EXISTS amount_basis TEXT CHECK (amount_basis IN ('legacy_window'));

UPDATE purchase_history ph
SET amount_basis = 'legacy_window'
WHERE ph.amount_basis IS NULL
  -- predicate-begin
  AND lower(ph.provider) = 'azure'
  AND (
    ph.timestamp < COALESCE((
      SELECT CASE WHEN min(sig.timestamp) IS NULL THEN NULL
             ELSE GREATEST(min(sig.timestamp), TIMESTAMPTZ '2026-10-08 09:15:51+00') END
      FROM purchase_history sig
      WHERE lower(sig.provider) = 'azure'
        AND lower(sig.payment) = 'monthly'
        AND sig.upfront_cost = 0
        AND sig.monthly_cost > 0
    ), 'infinity'::timestamptz)
    OR (lower(ph.payment) IN ('monthly', 'no-upfront', 'partial-upfront') AND ph.upfront_cost > 0)
  )
  -- predicate-end
;
