-- Quiesce old Azure collectors and deploy the fixed collector with this migration.
-- Rows collected before migration, even with the fix, need a successful recollection.
UPDATE recommendations
   SET payload = jsonb_set(jsonb_set(payload, '{upfront_cost}', 'null'::jsonb),
                           '{monthly_cost}', 'null'::jsonb)
 WHERE payload->>'provider' = 'azure';
