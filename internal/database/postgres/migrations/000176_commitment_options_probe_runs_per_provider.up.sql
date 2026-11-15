-- Migration 000176: one probe-run row per provider (issue #17).
--
-- commitment_options_probe_runs was a singleton (000039) that meant "the AWS
-- probe ran". The existing row becomes provider = 'aws' through the column
-- default (dropped right after, so an insert that forgets provider fails), so AWS stays warm and is not re-probed. Dropping the singleton
-- column drops its primary key and check with it.
BEGIN;

ALTER TABLE commitment_options_probe_runs ADD COLUMN provider TEXT NOT NULL DEFAULT 'aws';
ALTER TABLE commitment_options_probe_runs ALTER COLUMN provider DROP DEFAULT;
ALTER TABLE commitment_options_probe_runs DROP COLUMN singleton;
ALTER TABLE commitment_options_probe_runs ADD PRIMARY KEY (provider);

COMMIT;
