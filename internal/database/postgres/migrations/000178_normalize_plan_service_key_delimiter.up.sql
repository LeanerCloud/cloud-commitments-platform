-- Normalize the purchase_plans.services map keys to the "provider/service"
-- form (issue #7).
--
-- Migration 000040 wrote the split Savings Plans entries under
-- "provider:service" keys (e.g. "aws:savings-plans-compute"), while the plan
-- API (buildServiceConfig) writes "provider/service". Readers accept both
-- (findServiceKey), so this only removes the second spelling from stored data.
--
-- Only the key changes; every value is carried over byte for byte. Plan
-- resolution and ramp_ledger_shape read the entry values, not the keys, so the
-- ramp ledger state and baseline are untouched. The update_purchase_plans_updated_at
-- trigger still bumps updated_at on a rewritten row, so a plan snapshot read
-- before the migration fails its optimistic-lock check instead of writing the
-- old key back.
--
-- A key is rewritten only when it has the exact shape <token>:<token> (one
-- colon, no slash). When the "/" twin already exists in the same plan, the
-- colon entry is dropped if both values are identical; if they differ the
-- migration raises instead of choosing one, so a human decides.
--
-- Detection query for live databases (read only):
--
--   SELECT p.id, e.key
--     FROM purchase_plans p
--     CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(p.services) = 'object'
--                                        THEN p.services ELSE '{}'::jsonb END) e
--    WHERE e.key ~ '^[A-Za-z0-9_-]+:[A-Za-z0-9_-]+$'
--    ORDER BY p.id, e.key;
--
-- Idempotent: a second run finds no colon key. The helper function is dropped
-- before the migration commits.

CREATE FUNCTION normalize_plan_service_keys_000178(services JSONB) RETURNS JSONB
LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    IF services IS NULL OR jsonb_typeof(services) <> 'object' THEN
        RETURN services;
    END IF;
    RETURN COALESCE((
        SELECT jsonb_object_agg(
                   CASE WHEN e.key ~ '^[A-Za-z0-9_-]+:[A-Za-z0-9_-]+$'
                        THEN replace(e.key, ':', '/') ELSE e.key END,
                   e.value)
          FROM jsonb_each(services) e
    ), services);
END
$$;

DO $$
DECLARE
    conflicting TEXT;
BEGIN
    SELECT string_agg(DISTINCT p.id::text, ', ') INTO conflicting
      FROM purchase_plans p
      CROSS JOIN LATERAL jsonb_each(CASE WHEN jsonb_typeof(p.services) = 'object'
                                         THEN p.services ELSE '{}'::jsonb END) c
     WHERE c.key ~ '^[A-Za-z0-9_-]+:[A-Za-z0-9_-]+$'
       AND (p.services -> replace(c.key, ':', '/')) IS NOT NULL
       AND (p.services -> replace(c.key, ':', '/')) IS DISTINCT FROM c.value;
    IF conflicting IS NOT NULL THEN
        RAISE EXCEPTION 'purchase_plans.services has a "provider:service" and a "provider/service" entry with different values; resolve these plans by hand, then re-run: %', conflicting;
    END IF;
END
$$;

UPDATE purchase_plans
SET services = normalize_plan_service_keys_000178(services)
WHERE services IS DISTINCT FROM normalize_plan_service_keys_000178(services);

DROP FUNCTION normalize_plan_service_keys_000178(JSONB);
