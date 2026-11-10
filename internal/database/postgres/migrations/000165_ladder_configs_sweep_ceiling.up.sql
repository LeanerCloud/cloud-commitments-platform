-- Rolling-24h ceiling on the commitment the ladder claim may fire, in USD per
-- hour of commitment (the unit of amount_usd_hr). NULL means not set: the
-- ladder sweep refuses to buy, it never means unlimited.
ALTER TABLE ladder_configs
    ADD COLUMN IF NOT EXISTS max_hourly_commit_per_sweep NUMERIC(20,6) NULL
    CHECK (max_hourly_commit_per_sweep IS NULL OR max_hourly_commit_per_sweep > 0);
