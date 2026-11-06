-- Migration 000161: durable purchase attempt journal (cloud-commitments-go#107).
--
-- One row per (idempotency token, execution): the Redshift provider records
-- "sending" immediately before the AWS call and "purchased" or
-- "outcome_unknown" after it, so a later attempt of the same token can adopt a
-- recorded reservation instead of buying twice. The store
-- (RecordAttempt in internal/config/store_postgres_attempts.go) enforces the
-- transition table in its upsert; this table only pins the value set.
--
--   state           MUST match common.AttemptSending / AttemptPurchased /
--                   AttemptOutcomeUnknown in cloud-commitments-go
--                   pkg/common/attempt_journal.go byte for byte.
--   reservation_id  the provider commitment ID; NULL unless state = 'purchased'.
--   offering_id     the offering the attempt was sent under; NULL when unknown,
--                   written once (the upsert keeps the first non-NULL value).
CREATE TABLE IF NOT EXISTS purchase_attempts (
    idempotency_token TEXT NOT NULL CHECK (idempotency_token <> ''),
    execution_id TEXT NOT NULL CHECK (execution_id <> ''),
    state TEXT NOT NULL CHECK (state IN ('sending', 'purchased', 'outcome_unknown')),
    reservation_id TEXT NULL,
    offering_id TEXT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (idempotency_token, execution_id),
    CONSTRAINT purchase_attempts_reservation_iff_purchased
        CHECK ((state = 'purchased') = (reservation_id IS NOT NULL))
);

CREATE INDEX IF NOT EXISTS idx_purchase_attempts_token
    ON purchase_attempts (idempotency_token);
