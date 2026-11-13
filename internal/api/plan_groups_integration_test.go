//go:build integration

package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/email"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/planstep"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/scheduler"
)

// A plan created from a selection buys only the selected recommendations, and a
// group with no recommendations is refused (issue #1746).
func TestCreatePlanFromGroups_BuysOnlyTheSelection(t *testing.T) {
	f := prepareCombinedPlanFixture(t, false, nil, "USD")
	ctx := context.Background()
	monthly := 1000.0
	require.NoError(t, f.store.UpsertRecommendations(ctx, time.Now().UTC(), []config.RecommendationRecord{{
		ID: "unselected-azure-rec", Provider: "azure", Service: "compute", Region: "northeurope", ResourceType: "Standard_D4s_v3",
		Count: 50, Term: 1, Payment: "monthly", MonthlyCost: &monthly, Savings: 300, CloudAccountID: &f.account,
		CommitmentCurrency: "USD",
	}}, nil))

	refused := combinedRoute(t, f.handler, "POST", "/api/plans", PlanRequest{
		Name: "empty group", TargetAccounts: []string{f.account}, RampSchedule: "weekly-25pct",
		Groups: []PlanGroupRequest{{Provider: "azure", Service: "compute", Term: ptr(1), Payment: ptr("monthly"), RecommendationIDs: []string{}}},
	})
	require.Equal(t, 400, refused.StatusCode, refused.Body)

	response := combinedRoute(t, f.handler, "POST", "/api/plans", PlanRequest{
		Name: "from selection", TargetAccounts: []string{f.account}, RampSchedule: "weekly-25pct", Enabled: true,
		Groups: []PlanGroupRequest{{Provider: "azure", Service: "compute", Term: ptr(1), Payment: ptr("monthly"), Coverage: ptr(80), RecommendationIDs: []string{"combined-azure-rec"}}},
	})
	require.Equal(t, 200, response.StatusCode, response.Body)
	var created config.PurchasePlan
	require.NoError(t, json.Unmarshal([]byte(response.Body), &created))

	stored, err := f.store.GetPurchasePlan(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, config.RampAllocationBoundGroups, stored.RampSchedule.AllocationMode)
	require.Len(t, stored.Services, 1)
	for _, svc := range stored.Services {
		require.Len(t, svc.BoundIdentities, 1, "the unselected rec is not bound")
	}

	factory := combinedFactory{provider: &combinedProvider{service: &combinedService{store: f.store}}}
	manager := purchase.NewManager(purchase.ManagerConfig{ConfigStore: f.store, EmailSender: email.NewNopSender(), CredentialStore: &combinedCredentials{}, ProviderFactory: factory})
	source := scheduler.NewScheduler(scheduler.SchedulerConfig{ConfigStore: f.store, PurchaseManager: manager, EmailSender: email.NewNopSender(), ProviderFactory: factory})
	result, err := planstep.Resolve(ctx, source, f.store, stored, []string{f.account}, 1, nil, nil)
	require.NoError(t, err)
	require.Len(t, result.Recs, 1)
	assert.Equal(t, "combined-azure-rec", result.Recs[0].ID)
}
