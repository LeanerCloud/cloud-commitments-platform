ALTER TABLE ladder_tranches ADD COLUMN revision BIGINT NOT NULL DEFAULT 0 CHECK (revision >= 0);

CREATE TABLE ladder_tranche_amendments (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    tranche_id UUID NOT NULL REFERENCES ladder_tranches(id),
    actor TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    previous_revision BIGINT NOT NULL,
    new_revision BIGINT NOT NULL,
    previous_amount_usd_hr NUMERIC(20,6) NOT NULL,
    new_amount_usd_hr NUMERIC(20,6) NOT NULL,
    previous_scheduled_date TIMESTAMPTZ NOT NULL,
    new_scheduled_date TIMESTAMPTZ NOT NULL,
    UNIQUE (tranche_id, new_revision),
    CHECK (new_revision = previous_revision + 1)
);
