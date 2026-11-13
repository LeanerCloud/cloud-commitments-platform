# Azure legacy amount basis (issue #669)

Migration `purchase_history_amount_basis` flags pre-fix Azure `purchase_history`
rows with `amount_basis = 'legacy_window'`. Their `upfront_cost` and
`estimated_savings` were estimated over a window, so dollar totals skip them.
The rows stay listed and counted.

## Before dispatching the migration: read-only boundary query

Run on prod. It touches nothing. The `WHERE` between the markers is the exact
predicate the migration applies (a test keeps them identical). Check
`latest_flagged` against the time of the f2d60df8 deploy: it must not be later.

```sql
-- predicate-query
SELECT count(*) AS flagged_rows, min(ph.timestamp) AS earliest_flagged, max(ph.timestamp) AS latest_flagged,
       (SELECT min(sig.timestamp) FROM purchase_history sig
         WHERE lower(sig.provider) = 'azure' AND lower(sig.payment) = 'monthly'
           AND sig.upfront_cost = 0 AND sig.monthly_cost > 0) AS first_signature_row
FROM purchase_history ph
WHERE TRUE
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
```

## After the migration: manual un-flag

If the boundary is wrong, un-flag rows at or after the real cutover time
(replace the timestamp). Totals recover on the next read.

```sql
-- unflag
UPDATE purchase_history
SET amount_basis = NULL
WHERE amount_basis = 'legacy_window'
  AND lower(provider) = 'azure'
  AND timestamp >= TIMESTAMPTZ '2026-10-08 09:15:51+00';
```

## Not repaired by the migration

`savings_snapshots` and rollups already written keep the pre-fix amounts, so
trend charts step down at the deploy. See `known_issues/`.
