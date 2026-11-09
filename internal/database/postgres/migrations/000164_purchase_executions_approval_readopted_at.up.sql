ALTER TABLE purchase_executions ADD COLUMN IF NOT EXISTS approval_readopted_at timestamptz NULL;
