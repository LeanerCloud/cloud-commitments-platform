-- Record when a marketplace listing first reached a terminal state (issue #1586).
--
-- The poller keeps reading a closed or cancelled listing until its proceeds are
-- recorded. A listing whose sale price AWS never reported would be polled
-- forever, and listed_at (the AWS CreateDate) cannot bound that: a long-lived
-- listing can sell and close yesterday. terminal_at is the missing clock: set
-- once, when the listing first becomes closed or cancelled, and NULL while it
-- is active or pending. The poller stops reading an unpriced terminal listing
-- MarketplaceTerminalPollCutoffDays after terminal_at; a terminal row with a
-- NULL terminal_at is never cut off.
--
-- Triggers stamp it so every writer (settle, cancel, relist, claim release)
-- is covered in the same statement that changes the state. On
-- purchase_history, which holds the RI's current listing, it is cleared when
-- the row moves to a different or non-terminal listing; the superseded
-- listing keeps its own terminal_at in marketplace_listing_settlements.
--
-- Backfill: every existing terminal listing gets now(), a fresh full window.
-- That is deliberately conservative: the true close time is unknown and no
-- listing may be dropped earlier than its cutoff.

ALTER TABLE purchase_history
    ADD COLUMN IF NOT EXISTS terminal_at TIMESTAMPTZ;
ALTER TABLE marketplace_listing_settlements
    ADD COLUMN IF NOT EXISTS terminal_at TIMESTAMPTZ;

UPDATE purchase_history
SET terminal_at = now()
WHERE terminal_at IS NULL
  AND COALESCE(listing_id, '') <> ''
  AND lower(COALESCE(listing_state, '')) IN ('closed', 'cancelled');

UPDATE marketplace_listing_settlements
SET terminal_at = now()
WHERE terminal_at IS NULL
  AND lower(status) IN ('closed', 'cancelled');

CREATE OR REPLACE FUNCTION stamp_purchase_history_listing_terminal_at()
RETURNS TRIGGER AS $$
BEGIN
    IF lower(COALESCE(NEW.listing_state, '')) IN ('closed', 'cancelled')
       AND COALESCE(NEW.listing_id, '') <> '' THEN
        IF TG_OP = 'UPDATE' AND NEW.listing_id IS NOT DISTINCT FROM OLD.listing_id THEN
            NEW.terminal_at := COALESCE(OLD.terminal_at, now());
        ELSE
            NEW.terminal_at := now();
        END IF;
    ELSE
        NEW.terminal_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS purchase_history_stamp_listing_terminal_at ON purchase_history;
CREATE TRIGGER purchase_history_stamp_listing_terminal_at
    BEFORE INSERT OR UPDATE OF listing_id, listing_state ON purchase_history
    FOR EACH ROW
    EXECUTE FUNCTION stamp_purchase_history_listing_terminal_at();

CREATE OR REPLACE FUNCTION stamp_marketplace_settlement_terminal_at()
RETURNS TRIGGER AS $$
BEGIN
    IF lower(NEW.status) IN ('closed', 'cancelled') THEN
        NEW.terminal_at := COALESCE(CASE WHEN TG_OP = 'UPDATE' THEN OLD.terminal_at END,
                                    NEW.terminal_at, now());
    ELSE
        NEW.terminal_at := NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS marketplace_listing_settlements_stamp_terminal_at ON marketplace_listing_settlements;
CREATE TRIGGER marketplace_listing_settlements_stamp_terminal_at
    BEFORE INSERT OR UPDATE ON marketplace_listing_settlements
    FOR EACH ROW
    EXECUTE FUNCTION stamp_marketplace_settlement_terminal_at();

-- A superseded listing keeps the terminal_at of the purchase_history row it
-- came from (000153's function plus that column).
CREATE OR REPLACE FUNCTION keep_superseded_marketplace_listing()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO marketplace_listing_settlements (
        listing_id, purchase_history_id, purchase_id, status, listed_at,
        proceeds_received, fee_paid, settled_at, terminal_at
    ) VALUES (
        OLD.listing_id, OLD.id, OLD.purchase_id, lower(OLD.listing_state), OLD.listed_at,
        OLD.listing_proceeds_received,
        CASE WHEN OLD.listing_proceeds_received IS NOT NULL THEN COALESCE(OLD.listing_fee_paid, 0) END,
        CASE WHEN OLD.listing_proceeds_received IS NOT NULL THEN now() END,
        OLD.terminal_at
    )
    ON CONFLICT (listing_id) DO NOTHING;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
