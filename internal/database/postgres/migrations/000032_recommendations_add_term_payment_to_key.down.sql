-- Reverse of 000032: restore the original 5-column natural key.
--
-- Order matters: DROP INDEX (the new 7-tuple) → DROP COLUMN (otherwise
-- column-drop fails because the index references them) → CREATE INDEX
-- (the original 5-tuple shape). Reuses the same index name as the up
-- migration, matching the pre-000032 schema exactly.
--
-- Rows that differ only by `term` or `payment_option` would collide on the
-- narrower 5-column key and fail CREATE UNIQUE INDEX (issue #1769).
-- `recommendations` is a refreshable cache, so the DELETE below keeps one
-- row per 5-column key: the newest `collected_at`, ties broken by highest
-- `id`, which makes a re-run pick the same winner.

DELETE FROM recommendations
WHERE id IN (
    SELECT id FROM (
        SELECT id, ROW_NUMBER() OVER (
            PARTITION BY account_key, provider, service, region, resource_type
            ORDER BY collected_at DESC, id DESC
        ) AS rn
        FROM recommendations
    ) ranked
    WHERE rn > 1
);

DROP INDEX IF EXISTS recommendations_natural_key_idx;

ALTER TABLE recommendations
    DROP COLUMN IF EXISTS payment_option,
    DROP COLUMN IF EXISTS term;

CREATE UNIQUE INDEX recommendations_natural_key_idx
    ON recommendations (account_key, provider, service, region, resource_type);
