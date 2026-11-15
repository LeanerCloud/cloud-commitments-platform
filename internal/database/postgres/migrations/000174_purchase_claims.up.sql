-- Migration 000174: durable cross-process purchase claim (cloud-commitments-go#745).
--
-- One row per idempotency token. The Azure reservation flow takes the claim before
-- it lists or buys, so two replicas cannot both buy a token. The store
-- (internal/config/store_postgres_claims.go) does every transition in one
-- conditional statement and reads every instant from the database clock.
--
--   phase        MUST match common.ClaimClaimed / ClaimSending / ClaimCompleted in
--                cloud-commitments-go pkg/common/purchase_claim.go byte for byte.
--   generation   the fencing token: bumped on every takeover; Renew, Advance and
--                Release only match the (holder, generation) they were issued.
--   expires_at   lease end; last_renewed_at starts the settle window of a sending row.
--   order_id     the provider order; set exactly when phase = 'completed'.
CREATE TABLE IF NOT EXISTS purchase_claims (
    idempotency_token TEXT PRIMARY KEY CHECK (idempotency_token <> ''),
    holder TEXT NOT NULL CHECK (holder <> ''),
    generation BIGINT NOT NULL CHECK (generation >= 1),
    phase TEXT NOT NULL CHECK (phase IN ('claimed', 'sending', 'completed')),
    order_id TEXT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    last_renewed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT purchase_claims_order_iff_completed
        CHECK ((phase = 'completed') = (order_id IS NOT NULL AND order_id <> ''))
);
