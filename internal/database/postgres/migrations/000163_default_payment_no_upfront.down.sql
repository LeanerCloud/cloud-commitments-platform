-- Reverts 000163. Only a row that is still the untouched no-upfront seed
-- (updated_at = created_at) goes back; an admin's deliberate no-upfront choice
-- (trigger-stamped) is kept.
ALTER TABLE global_config ALTER COLUMN default_payment SET DEFAULT 'all-upfront';
ALTER TABLE service_configs ALTER COLUMN payment SET DEFAULT 'all-upfront';

ALTER TABLE global_config DISABLE TRIGGER update_global_config_updated_at;

UPDATE global_config
SET default_payment = 'all-upfront'
WHERE id = 1
  AND default_payment = 'no-upfront'
  AND updated_at = created_at;

ALTER TABLE global_config ENABLE TRIGGER update_global_config_updated_at;
