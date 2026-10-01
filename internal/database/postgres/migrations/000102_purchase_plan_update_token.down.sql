CREATE OR REPLACE TRIGGER update_purchase_plans_updated_at
    BEFORE UPDATE ON purchase_plans
    FOR EACH ROW EXECUTE FUNCTION update_updated_at_column();

DROP FUNCTION update_purchase_plan_timestamp();
