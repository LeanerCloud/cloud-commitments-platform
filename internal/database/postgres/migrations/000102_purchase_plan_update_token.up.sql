CREATE OR REPLACE FUNCTION update_purchase_plan_timestamp()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = GREATEST(clock_timestamp(), OLD.updated_at + INTERVAL '1 microsecond');
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER update_purchase_plans_updated_at
    BEFORE UPDATE ON purchase_plans
    FOR EACH ROW EXECUTE FUNCTION update_purchase_plan_timestamp();
