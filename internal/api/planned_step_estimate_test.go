package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const estPlanID = "51515151-5151-5151-5151-515151515151"

type estimateFixture struct {
	handler *Handler
	store   *MockConfigStore
	sched   *MockScheduler
	req     *events.LambdaFunctionURLRequest
	stored  config.RecommendationRecord
}

func estimatePlan(id string, currentStep int) config.PurchasePlan {
	return config.PurchasePlan{
		ID:   id,
		Name: "Azure weekly",
		Services: map[string]config.ServiceConfig{
			"azure:compute": {Provider: "azure", Service: "compute", Enabled: true, Term: 1, Payment: "no-upfront", Coverage: 80},
		},
		RampSchedule: config.RampSchedule{Type: "weekly", PercentPerStep: 25, TotalSteps: 4, CurrentStep: currentStep},
	}
}

func estimateRoot(planID string, step int) config.PurchaseExecution {
	return config.PurchaseExecution{
		ExecutionID: "6161616" + string(rune('0'+step)) + "-6161-6161-6161-616161616161", PlanID: planID,
		Status: "pending", StepNumber: step, ScheduledDate: time.Now().AddDate(0, 0, 7*step),
	}
}

// newEstimateFixture wires a caller holding view:plans and (canViewRecs)
// view:recommendations over the given plans and root rows. The stored rec is
// the Azure shape of the reported plan: 20 units, one account.
func newEstimateFixture(t *testing.T, canViewRecs bool, plans []config.PurchasePlan, execs []config.PurchaseExecution) *estimateFixture {
	t.Helper()
	acct, monthly := stepAcct, 100.0
	f := &estimateFixture{store: new(MockConfigStore), sched: new(MockScheduler)}
	f.stored = config.RecommendationRecord{
		ID: "azure|acct-step|compute|westeurope|Standard_D2s_v3||1|monthly", Provider: "azure", Service: "compute",
		Region: "westeurope", ResourceType: "Standard_D2s_v3", Term: 1, Payment: "monthly", Count: 20,
		UpfrontCost: 50, Savings: 10, MonthlyCost: &monthly, CloudAccountID: &acct, CommitmentCurrency: "USD",
	}
	auth := new(MockAuthService)
	auth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: "u-1"}, nil)
	auth.On("HasPermissionAPI", mock.Anything, "u-1", "view", "plans").Return(true, nil)
	auth.On("HasPermissionAPI", mock.Anything, "u-1", "view", "recommendations").Return(canViewRecs, nil)
	auth.On("GetAllowedAccountsAPI", mock.Anything, "u-1").Return([]string(nil), nil)
	f.store.On("GetPlannedExecutions", mock.Anything, mock.Anything, mock.Anything).Return(execs, nil)
	f.store.On("ListPurchasePlans", mock.Anything, mock.Anything).Return(plans, nil)
	f.store.On("GetPlanAccounts", mock.Anything, mock.Anything).Return([]config.CloudAccount{{ID: stepAcct}}, nil).Maybe()
	f.store.On("ListStoredRecommendations", mock.Anything, mock.Anything).Return([]config.RecommendationRecord{f.stored}, nil).Maybe()
	f.sched.On("ListRecommendationsStrict", mock.Anything, mock.Anything).Return([]config.RecommendationRecord{f.stored}, nil).Maybe()
	f.handler = &Handler{config: f.store, auth: auth, scheduler: f.sched}
	f.req = &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}}
	return f
}

func (f *estimateFixture) rows(t *testing.T) []PlannedPurchase {
	t.Helper()
	res, err := f.handler.getPlannedPurchases(context.Background(), f.req)
	require.NoError(t, err)
	return res.Purchases
}

// resolved is what the scheduler would attach for the step, from the same planstep.Resolve.
func (f *estimateFixture) resolved(t *testing.T, plan *config.PurchasePlan, step int, baseline config.PlanRampBaseline, committed map[string]config.PlanRampQuantity) planstep.Result {
	t.Helper()
	res, err := planstep.Resolve(context.Background(), stepSource{[]config.RecommendationRecord{f.stored}}, stepStore{[]config.RecommendationRecord{f.stored}}, plan, []string{stepAcct}, step, baseline, committed)
	require.NoError(t, err)
	return res
}

func TestPlannedPurchases_EstimatesNextUnattachedStep(t *testing.T) {
	plan := estimatePlan(estPlanID, 0)
	f := newEstimateFixture(t, true, []config.PurchasePlan{plan}, []config.PurchaseExecution{estimateRoot(estPlanID, 1)})
	want := f.resolved(t, &plan, 1, nil, nil)

	rows := f.rows(t)
	require.Len(t, rows, 1)
	got := rows[0]
	assert.True(t, got.Estimate)
	assert.False(t, got.HasRecommendations, "an estimate is not an attached recommendation")
	wantCount, wantUpfront, wantSavings := 0, 0.0, 0.0
	for _, r := range want.Recs {
		wantCount, wantUpfront, wantSavings = wantCount+r.Count, wantUpfront+r.UpfrontCost, wantSavings+r.Savings
	}
	assert.Equal(t, 4, wantCount, "20 shown x 80% x 25%")
	assert.Equal(t, wantCount, got.Count)
	assert.InDelta(t, wantUpfront, got.UpfrontCost, 1e-9)
	assert.InDelta(t, wantSavings, got.EstimatedSavings, 1e-9)
	assert.Equal(t, "Standard_D2s_v3", got.ResourceType)
	assert.Equal(t, "westeurope", got.Region)
}

func TestPlannedPurchases_EstimateSizesCumulativeMinusCommittedAndOnlyNextStep(t *testing.T) {
	plan := estimatePlan(estPlanID, 1)
	f := newEstimateFixture(t, true, []config.PurchasePlan{plan}, []config.PurchaseExecution{estimateRoot(estPlanID, 2), estimateRoot(estPlanID, 3)})
	key := f.resolved(t, &plan, 1, nil, nil).Baseline
	require.Len(t, key, 1)
	var id string
	for k := range key {
		id = k
	}
	f.store.On("GetPlanRampBaselineTx", mock.Anything, mock.Anything, estPlanID).Return(config.PlanRampBaseline{id: {Count: 20}}, nil)
	f.store.On("ListPlanCommittedTx", mock.Anything, mock.Anything, estPlanID).Return(map[string]config.PlanRampQuantity{id: {Count: 2}}, nil)

	rows := f.rows(t)
	require.Len(t, rows, 2)
	assert.True(t, rows[0].Estimate)
	assert.Equal(t, 6, rows[0].Count, "step 2 target 8 minus 2 committed")
	assert.False(t, rows[1].Estimate, "step 3 would overstate: step 2 is not in the ledger yet")
	assert.Equal(t, "sized when step 2 runs", rows[1].EstimateNote)
}

func TestPlannedPurchases_EstimateNotesInsteadOfZeros(t *testing.T) {
	boom := errors.New("db down")
	cases := []struct {
		name  string
		setup func(*estimateFixture, *config.PurchasePlan)
		note  string
	}{
		{"no match", func(f *estimateFixture, _ *config.PurchasePlan) {
			f.sched.ExpectedCalls = nil
			f.sched.On("ListRecommendationsStrict", mock.Anything, mock.Anything).Return([]config.RecommendationRecord{}, nil)
		}, "no matching recommendation right now"},
		{"unsupported plan", func(_ *estimateFixture, p *config.PurchasePlan) {
			p.Services["azure:compute"] = config.ServiceConfig{Provider: "azure", Service: "compute", Enabled: true, Term: 1, Payment: "no-upfront"}
		}, "coverage"},
		{"cap exceeded", func(_ *estimateFixture, p *config.PurchasePlan) {
			limit := 0.01
			p.MaxStepCommitment = &limit
		}, "cap"},
		{"strict read fails", func(f *estimateFixture, _ *config.PurchasePlan) {
			f.sched.ExpectedCalls = nil
			f.sched.On("ListRecommendationsStrict", mock.Anything, mock.Anything).Return(nil, boom)
		}, "estimate unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := estimatePlan(estPlanID, 0)
			f := newEstimateFixture(t, true, nil, []config.PurchaseExecution{estimateRoot(estPlanID, 1)})
			tc.setup(f, &plan)
			f.store.ExpectedCalls = filterCalls(f.store.ExpectedCalls, "ListPurchasePlans")
			f.store.On("ListPurchasePlans", mock.Anything, mock.Anything).Return([]config.PurchasePlan{plan}, nil)

			rows := f.rows(t)
			require.Len(t, rows, 1)
			assert.False(t, rows[0].Estimate)
			assert.Zero(t, rows[0].Count)
			assert.Zero(t, rows[0].UpfrontCost+rows[0].EstimatedSavings, "no figures with a note")
			assert.Contains(t, rows[0].EstimateNote, tc.note)
		})
	}
}

func filterCalls(calls []*mock.Call, drop string) []*mock.Call {
	var kept []*mock.Call
	for _, c := range calls {
		if c.Method != drop {
			kept = append(kept, c)
		}
	}
	return kept
}

func TestPlannedPurchases_NoEstimateWithoutViewRecommendations(t *testing.T) {
	plan := estimatePlan(estPlanID, 0)
	f := newEstimateFixture(t, false, []config.PurchasePlan{plan}, []config.PurchaseExecution{estimateRoot(estPlanID, 1), estimateRoot(estPlanID, 2)})

	for _, row := range f.rows(t) {
		assert.False(t, row.Estimate)
		assert.Empty(t, row.EstimateNote)
		assert.Zero(t, row.Count)
	}
	assert.Empty(t, f.sched.Calls, "no recommendation read without the permission")
}

func TestPlannedPurchases_EstimateFailureOnOnePlanKeepsOthers(t *testing.T) {
	planA, planB := estimatePlan(estPlanID, 0), estimatePlan("71717171-7171-7171-7171-717171717171", 0)
	f := newEstimateFixture(t, true, []config.PurchasePlan{planA, planB},
		[]config.PurchaseExecution{estimateRoot(planA.ID, 1), estimateRoot(planB.ID, 1)})
	f.store.ExpectedCalls = filterCalls(f.store.ExpectedCalls, "GetPlanAccounts")
	f.store.On("GetPlanAccounts", mock.Anything, planA.ID).Return(nil, errors.New("accounts unreadable"))
	f.store.On("GetPlanAccounts", mock.Anything, planB.ID).Return([]config.CloudAccount{{ID: stepAcct}}, nil)

	rows := f.rows(t)
	require.Len(t, rows, 2)
	assert.Equal(t, "estimate unavailable", rows[0].EstimateNote)
	assert.True(t, rows[1].Estimate)
	assert.Equal(t, 4, rows[1].Count)
}

func TestPlannedPurchases_EstimateNeverWrites(t *testing.T) {
	plan := estimatePlan(estPlanID, 0)
	f := newEstimateFixture(t, true, []config.PurchasePlan{plan}, []config.PurchaseExecution{estimateRoot(estPlanID, 1)})

	rows := f.rows(t)
	require.True(t, rows[0].Estimate)
	store := f.store
	store.AssertNotCalled(t, "SetPlanRampBaselineTx", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SavePurchaseExecutionTx", mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "SetExecutionRecommendationsIfEmptyTx", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}
