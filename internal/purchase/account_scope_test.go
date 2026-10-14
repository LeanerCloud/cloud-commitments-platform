package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withAccount(rec config.RecommendationRecord, id string) config.RecommendationRecord {
	rec.CloudAccountID = &id
	return rec
}

func scopedTestRec(accountID string) config.RecommendationRecord {
	return withAccount(config.RecommendationRecord{
		Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1",
		Count: 1, UpfrontCost: 300, Savings: 75, Selected: true,
	}, accountID)
}

func scopeFixture() (*config.PurchaseExecution, []config.CloudAccount) {
	a, b := scopedTestRec("acct-A"), scopedTestRec("acct-B")
	b.ResourceType, b.UpfrontCost, b.Savings = "c5.large", 100, 20
	return &config.PurchaseExecution{
		ExecutionID: "root", PlanID: "plan-x", StepNumber: 1,
		TotalUpfrontCost: 400, EstimatedSavings: 95,
		Recommendations: []config.RecommendationRecord{a, b},
	}, []config.CloudAccount{{ID: "acct-A"}, {ID: "acct-B"}}
}

// Each account gets only its own recs, and its row carries its own totals
// (not the step's, which would show N times the money in History).
func TestScopeExecutionsByAccount_OwnRecsAndTotals(t *testing.T) {
	base, accounts := scopeFixture()
	scoped, err := scopeExecutionsByAccount(base, accounts)
	require.NoError(t, err)
	require.Len(t, scoped, 2)
	assert.Len(t, scoped["acct-A"].Recommendations, 1)
	assert.Equal(t, "m5.large", scoped["acct-A"].Recommendations[0].ResourceType)
	assert.Equal(t, 300.0, scoped["acct-A"].TotalUpfrontCost)
	assert.Equal(t, 75.0, scoped["acct-A"].EstimatedSavings)
	assert.Equal(t, 100.0, scoped["acct-B"].TotalUpfrontCost)
	assert.Equal(t, 20.0, scoped["acct-B"].EstimatedSavings)
	assert.Len(t, base.Recommendations, 2, "base execution must be untouched")
}

// A rec with no account, or one outside the plan, fails the step loudly.
func TestScopeExecutionsByAccount_StrayRecFailsLoudly(t *testing.T) {
	for name, stray := range map[string]config.RecommendationRecord{
		"nil account":     {Provider: "azure", Service: "compute", ResourceType: "D2"},
		"foreign account": scopedTestRec("acct-other"),
	} {
		t.Run(name, func(t *testing.T) {
			base, accounts := scopeFixture()
			base.Recommendations = append(base.Recommendations, stray)
			scoped, err := scopeExecutionsByAccount(base, accounts)
			require.Error(t, err)
			assert.Nil(t, scoped)
			assert.Contains(t, err.Error(), "not attributed to one of the plan's accounts")
		})
	}
}

// executeMultiAccount: an empty step must fail with ErrPlanStepNoRecommendations
// (nil would be recorded as completed and advance the ramp), buying nothing.
func TestExecuteMultiAccount_EmptyStepFailsAndBuysNothing(t *testing.T) {
	base, accounts := scopeFixture()
	base.Recommendations = nil
	store := new(MockConfigStore)
	m := &Manager{config: store}

	err := m.executeMultiAccount(context.Background(), base, &config.PurchasePlan{ID: "plan-x"}, accounts)

	require.ErrorIs(t, err, ErrPlanStepNoRecommendations)
	store.AssertNotCalled(t, "SavePurchaseExecution")
}
