-- Migration 000108: one purchase_history row per (provider, account_id, purchase_id)
-- from now on (#704, MON-06).
--
-- A re-drive or root retry that adopts an existing commitment used to insert a
-- second row for it, double-counting spend and savings. SavePurchaseHistory
-- now skips a key that already exists; this index makes that hold under
-- concurrent saves.
--
-- Rows that exist today may already contain duplicates, and removing them
-- deletes financial history, which is an owner decision made separately. So
-- the index is partial: it covers only rows created after this migration runs
-- (the cutoff is evaluated once, here, and baked into the index predicate).
-- Existing rows, duplicates included, are untouched. SavePurchaseHistory's
-- NOT EXISTS check still stops a new save from duplicating an older row.
DO $$
BEGIN
    EXECUTE format(
        'CREATE UNIQUE INDEX IF NOT EXISTS uq_purchase_history_purchase '
        'ON purchase_history (provider, account_id, purchase_id) '
        'WHERE created_at > %L::timestamptz',
        now());
END $$;
