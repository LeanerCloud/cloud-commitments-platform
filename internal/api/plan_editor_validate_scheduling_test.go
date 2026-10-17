package api

import (
	"net/http"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestValidatePlanEditorScheduling(t *testing.T) {
	roots := []config.PlanScheduleRootChange{{StepNumber: 1}}
	accounts := []string{"a"}
	plan := func(mutate func(*config.PurchasePlan)) *config.PurchasePlan {
		p := editorPolicyBefore().Plan
		p.AutoPurchase = false
		p.Services = map[string]config.ServiceConfig{"k": {Provider: "aws", Service: "ec2", Enabled: true, Term: 1}}
		mutate(p)
		return p
	}
	run := func(p *config.PurchasePlan, global *config.GlobalConfig, accountIDs []string, r []config.PlanScheduleRootChange) error {
		store := new(MockConfigStore)
		store.On("GetGlobalConfig", mock.Anything).Return(global, nil).Maybe()
		return (&Handler{config: store}).validatePlanEditorScheduling(t.Context(), p, accountIDs, r)
	}
	enabledGlobal := &config.GlobalConfig{AutoPurchaseEnabled: true}
	unsupported := func(name string, p *config.PurchasePlan, global *config.GlobalConfig, accountIDs []string) {
		t.Run(name, func(t *testing.T) {
			err := run(p, global, accountIDs, roots)
			requireEditorPolicyRefusal(t, err, http.StatusConflict, codeEditorPlanUnsupported, name)
		})
	}
	unsupported("disabled plan", plan(func(p *config.PurchasePlan) { p.Enabled = false }), nil, accounts)
	unsupported("a filter on a scheduled ramp", plan(func(p *config.PurchasePlan) {
		svc := p.Services["k"]
		svc.IncludeRegions = []string{"us-east-1"}
		p.Services["k"] = svc
	}), nil, accounts)
	unsupported("immediate ramp with a partial percent", plan(func(p *config.PurchasePlan) { p.RampSchedule.Type, p.RampSchedule.PercentPerStep = "immediate", 50 }), nil, accounts)
	unsupported("multi-step immediate ramp", plan(func(p *config.PurchasePlan) {
		p.RampSchedule.Type, p.RampSchedule.PercentPerStep, p.RampSchedule.TotalSteps = "immediate", 100, 4
	}), nil, accounts)
	unsupported("zero-step immediate ramp", plan(func(p *config.PurchasePlan) {
		p.RampSchedule.Type, p.RampSchedule.PercentPerStep, p.RampSchedule.TotalSteps = "immediate", 100, 0
	}), nil, accounts)
	unsupported("no veto window", plan(func(p *config.PurchasePlan) { p.NotificationDaysBefore = 0 }), nil, accounts)
	unsupported("zero interval", plan(func(p *config.PurchasePlan) { p.RampSchedule.StepIntervalDays = 0 }), nil, accounts)
	unsupported("no accounts", plan(func(*config.PurchasePlan) {}), nil, nil)
	unsupported("armed without a cap", plan(func(p *config.PurchasePlan) { p.AutoPurchase, p.MaxStepCommitment = true, nil }), enabledGlobal, accounts)
	unsupported("armed with the global switch off", plan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), &config.GlobalConfig{}, accounts)
	t.Run("a valid plan passes, armed or not", func(t *testing.T) {
		require.NoError(t, run(plan(func(*config.PurchasePlan) {}), nil, accounts, roots))
		require.NoError(t, run(plan(func(p *config.PurchasePlan) { p.AutoPurchase = true }), enabledGlobal, accounts, roots))
	})
	t.Run("no roots is never blocked", func(t *testing.T) {
		bad := plan(func(p *config.PurchasePlan) { p.Enabled, p.NotificationDaysBefore = false, 0 })
		require.NoError(t, run(bad, nil, nil, nil))
	})
}
