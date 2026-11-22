-- Revert migration 000179 (issue #1586): drop terminal_at, its triggers, and
-- restore 000153's keep_superseded_marketplace_listing().
DROP TRIGGER IF EXISTS purchase_history_stamp_listing_terminal_at ON purchase_history;
DROP TRIGGER IF EXISTS marketplace_listing_settlements_stamp_terminal_at ON marketplace_listing_settlements;
DROP FUNCTION IF EXISTS stamp_purchase_history_listing_terminal_at();
DROP FUNCTION IF EXISTS stamp_marketplace_settlement_terminal_at();

CREATE OR REPLACE FUNCTION keep_superseded_marketplace_listing()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO marketplace_listing_settlements (
        listing_id, purchase_history_id, purchase_id, status, listed_at,
        proceeds_received, fee_paid, settled_at
    ) VALUES (
        OLD.listing_id, OLD.id, OLD.purchase_id, lower(OLD.listing_state), OLD.listed_at,
        OLD.listing_proceeds_received,
        CASE WHEN OLD.listing_proceeds_received IS NOT NULL THEN COALESCE(OLD.listing_fee_paid, 0) END,
        CASE WHEN OLD.listing_proceeds_received IS NOT NULL THEN now() END
    )
    ON CONFLICT (listing_id) DO NOTHING;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

ALTER TABLE marketplace_listing_settlements DROP COLUMN IF EXISTS terminal_at;
ALTER TABLE purchase_history DROP COLUMN IF EXISTS terminal_at;
