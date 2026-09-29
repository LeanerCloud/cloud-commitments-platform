ALTER TABLE purchase_history ALTER COLUMN upfront_cost DROP NOT NULL;
ALTER TABLE purchase_history ALTER COLUMN upfront_cost DROP DEFAULT;
ALTER TABLE savings_snapshots ALTER COLUMN total_commitment DROP NOT NULL;
ALTER TABLE savings_snapshots ALTER COLUMN total_commitment DROP DEFAULT;

-- Existing view commitment columns remain known-sample statistics, not complete totals.
-- Runtime APIs read nullable snapshots or aggregate purchase history directly.
