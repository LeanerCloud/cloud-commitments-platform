-- Migration 000105: persist the in-flight marketplace listing attempt (issue #525).
--
-- listing_client_token: the CreateReservedInstancesListing ClientToken of an
-- attempt that has not been resolved. It is written in the same UPDATE that
-- claims the listing slot, before the AWS call, so a retry after an ambiguous
-- AWS error or a crash reuses it and AWS returns the existing listing instead
-- of creating a duplicate.
-- listing_price_schedule: the price schedule that token was sent with. The
-- default schedule depends on the clock, and AWS rejects a reused token with
-- different parameters, so a retry must resend the stored schedule.
--
-- Both columns are NULL when no attempt is unresolved (legacy rows included)
-- and are cleared when a listing is recorded or the attempt is abandoned.

ALTER TABLE purchase_history
    ADD COLUMN IF NOT EXISTS listing_client_token   TEXT,
    ADD COLUMN IF NOT EXISTS listing_price_schedule JSONB;
