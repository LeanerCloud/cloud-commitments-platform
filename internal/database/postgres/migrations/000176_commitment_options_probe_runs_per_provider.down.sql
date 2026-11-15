-- 000176 down: restore the AWS-only singleton probe row.
--
-- A singleton cannot hold other providers, so their run rows and combos are
-- deleted after being copied into *_backup_000176 (kept until the owner drops
-- them). A later down after another up upserts the backup, so it holds the
-- newest run row per provider and the union of removed combos.
BEGIN;

CREATE TABLE IF NOT EXISTS commitment_options_probe_runs_backup_000176 (
    provider TEXT PRIMARY KEY,
    probed_at TIMESTAMPTZ NOT NULL,
    source_account_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS commitment_options_combos_backup_000176 (
    provider TEXT NOT NULL,
    service TEXT NOT NULL,
    term_years INT NOT NULL,
    payment_option TEXT NOT NULL,
    PRIMARY KEY (provider, service, term_years, payment_option)
);

INSERT INTO commitment_options_probe_runs_backup_000176
SELECT provider, probed_at, source_account_id FROM commitment_options_probe_runs WHERE provider <> 'aws'
ON CONFLICT (provider) DO UPDATE
   SET probed_at = EXCLUDED.probed_at, source_account_id = EXCLUDED.source_account_id;
INSERT INTO commitment_options_combos_backup_000176
SELECT provider, service, term_years, payment_option FROM commitment_options_combos WHERE provider <> 'aws'
ON CONFLICT DO NOTHING;

DELETE FROM commitment_options_probe_runs WHERE provider <> 'aws';
DELETE FROM commitment_options_combos WHERE provider <> 'aws';

ALTER TABLE commitment_options_probe_runs DROP CONSTRAINT commitment_options_probe_runs_pkey;
ALTER TABLE commitment_options_probe_runs DROP COLUMN provider;
ALTER TABLE commitment_options_probe_runs ADD COLUMN singleton BOOLEAN NOT NULL DEFAULT TRUE CHECK (singleton);
ALTER TABLE commitment_options_probe_runs ADD PRIMARY KEY (singleton);

COMMIT;
