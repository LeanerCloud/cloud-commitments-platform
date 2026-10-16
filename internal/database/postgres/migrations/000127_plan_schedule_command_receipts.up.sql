-- Immutable replay receipts for the atomic plan editor Save (#631). plan_id has no
-- foreign key: a receipt must stay replayable after its plan is deleted, and
-- retention (CleanupOldExecutions) is the only thing that removes it.
CREATE TABLE plan_schedule_command_receipts (
    command_id      UUID PRIMARY KEY,
    plan_id         UUID NOT NULL,
    actor_kind      TEXT NOT NULL CHECK (actor_kind IN ('session', 'user-api-key', 'admin-api-key')),
    actor_user_id   UUID,
    actor_key_id    UUID,
    request_hash    TEXT NOT NULL,
    base_revision   TEXT,
    result_revision TEXT NOT NULL,
    status_code     INTEGER NOT NULL CHECK (status_code IN (200, 201)),
    result_json     JSON NOT NULL,
    scope_evidence  JSONB NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_plan_schedule_command_receipts_created_at ON plan_schedule_command_receipts (created_at);

CREATE FUNCTION plan_schedule_command_receipts_immutable() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'plan schedule command receipts are immutable' USING ERRCODE = 'check_violation';
END
$$;

CREATE TRIGGER plan_schedule_command_receipts_no_update
    BEFORE UPDATE ON plan_schedule_command_receipts
    FOR EACH ROW EXECUTE FUNCTION plan_schedule_command_receipts_immutable();
