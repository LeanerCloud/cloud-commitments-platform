-- One immutable row per schedule-editor command (#1057). command_id is the
-- caller's idempotency key: a replay returns the stored outcome instead of
-- writing again. Rows are never updated; they go away only with their plan.
CREATE TABLE plan_schedule_command_receipts (
    command_id   UUID PRIMARY KEY,
    plan_id      UUID NOT NULL REFERENCES purchase_plans(id) ON DELETE CASCADE,
    actor        TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    outcome      JSONB NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_plan_schedule_command_receipts_plan_id
    ON plan_schedule_command_receipts (plan_id);

-- DELETE is refused while the plan exists; the FK cascade of a plan delete
-- runs after the plan row is gone, so it passes.
CREATE FUNCTION plan_schedule_command_receipts_immutable() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'plan_schedule_command_receipts rows are immutable';
    END IF;
    IF EXISTS (SELECT 1 FROM purchase_plans WHERE id = OLD.plan_id) THEN
        RAISE EXCEPTION 'plan_schedule_command_receipts rows are immutable';
    END IF;
    RETURN OLD;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER plan_schedule_command_receipts_immutable
    BEFORE UPDATE OR DELETE ON plan_schedule_command_receipts
    FOR EACH ROW EXECUTE FUNCTION plan_schedule_command_receipts_immutable();
