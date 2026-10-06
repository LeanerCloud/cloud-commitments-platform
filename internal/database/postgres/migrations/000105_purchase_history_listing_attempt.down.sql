-- Revert migration 000105
ALTER TABLE purchase_history
    DROP COLUMN IF EXISTS listing_client_token,
    DROP COLUMN IF EXISTS listing_price_schedule;
