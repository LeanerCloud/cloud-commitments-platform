BEGIN;

-- SET NOT NULL refuses rollback if unknown costs exist; never fabricate zeros.
ALTER TABLE purchase_history ALTER COLUMN upfront_cost SET NOT NULL;
ALTER TABLE savings_snapshots ALTER COLUMN total_commitment SET NOT NULL;
ALTER TABLE purchase_history ALTER COLUMN upfront_cost SET DEFAULT 0.00;
ALTER TABLE savings_snapshots ALTER COLUMN total_commitment SET DEFAULT 0.00;

COMMIT;
