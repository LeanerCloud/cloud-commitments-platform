package purchase

import (
	"context"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1048: a plan step whose plan was deleted must never fire. The delete leaves
// the row with plan_id NULL (ON DELETE SET NULL), so the guard recognizes a plan
// step by exclusion. Every test asserts on the provider call count and on the
// row the refusal saves.

// orphanSaves records every row the executor persists, so a test can read the
// terminal status and error the operator will see.
type orphanSaves struct {
	mu   sync.Mutex
	rows []config.PurchaseExecution
}

func (s *orphanSaves) last() config.PurchaseExecution {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) == 0 {
		return config.PurchaseExecution{}
	}
	return s.rows[len(s.rows)-1]
}

func captureSaves(store *MockConfigStore) *orphanSaves {
	s := &orphanSaves{}
	store.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.rows = append(s.rows, *e)
		return nil
	}
	return s
}

// orphanStep is a plan step as the sweep or plan creation minted it, after the
// plan's deletion nulled plan_id: no plan, no Source, no direct-execute marks.
func orphanStep(status string) *config.PurchaseExecution {
	exec := armedExecution("azure", "savingsplans", 0, status)
	exec.PlanID = ""
	exec.Source = ""
	exec.StepNumber = 1
	exec.CreatedBySystem = true
	return exec
}

// allowFreshAzurePricing makes the Azure pricing-freshness gate pass, as the
// first-purchase control in armed_redrive_test.go does, so a negative control
// reaches the provider.
func allowFreshAzurePricing(store *MockConfigStore, exec *config.PurchaseExecution) {
	exec.Recommendations[0].ID = "current-azure"
	store.On("ListStoredRecommendations", mock.Anything, config.RecommendationFilter{Provider: "azure", RequireAzurePricingMigration: true}).Return(exec.Recommendations, nil).Maybe()
}

func scheduledDue(store *MockConfigStore, exec *config.PurchaseExecution) {
	store.On("GetScheduledExecutionsDue", mock.Anything).Return([]config.PurchaseExecution{*exec}, nil).Once()
	expectClaim(store, exec, []string{"scheduled"})
}

func requirePlanGoneRefusal(t *testing.T, rec *armedPurchaseRecorder, saved *orphanSaves) {
	t.Helper()
	assert.Equal(t, 0, rec.count(), "a plan step whose plan is gone must buy nothing")
	row := saved.last()
	assert.Equal(t, config.StatusFailed, row.Status, "the refusal must be a visible failed row, not a silent skip or a delete")
	assert.Contains(t, row.Error, PlanGoneRefusal, "the failed row must record why")
	assert.True(t, row.NoSubmission, "the row must carry the proof that nothing was submitted")
}

// Case 2 of the issue: a scheduled row of a deleted plan, due now.
func TestFireScheduledRefusesStepOfDeletedPlan(t *testing.T) {
	exec := orphanStep("scheduled")
	mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
	saved := captureSaves(store)
	scheduledDue(store, exec)

	result, err := mgr.FireScheduledDelayedPurchases(context.Background())

	require.NoError(t, err)
	requirePlanGoneRefusal(t, rec, saved)
	assert.Equal(t, 0, result.Fired)
	assert.Equal(t, 1, result.Errored, "the refusal must count as an error the sweep reports")
}

// Case 1 of the issue: an open row orphaned before #1046, approved from the
// approval link or the queue.
func TestApproveAndExecuteRefusesOrphanedOpenStep(t *testing.T) {
	for _, status := range []string{"pending", "notified"} {
		t.Run(status, func(t *testing.T) {
			exec := orphanStep(status)
			mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
			saved := captureSaves(store)
			store.On("GetExecutionByID", mock.Anything, exec.ExecutionID).Return(exec, nil).Maybe()
			expectClaim(store, exec, []string{"pending", "notified"})

			_, _, err := mgr.ApproveAndExecute(context.Background(), exec.ExecutionID, "operator@example.com", nil, nil)

			require.Error(t, err)
			assert.Contains(t, err.Error(), PlanGoneRefusal)
			requirePlanGoneRefusal(t, rec, saved)
		})
	}
}

func TestClaimAndExecuteRefusesOrphanedOpenStep(t *testing.T) {
	exec := orphanStep("notified")
	mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
	saved := captureSaves(store)
	expectClaim(store, exec, []string{"pending", "notified"})

	claimed, err := mgr.claimAndExecuteWithin(context.Background(), exec, 0)

	require.True(t, claimed)
	require.Error(t, err)
	requirePlanGoneRefusal(t, rec, saved)
}

// A plan-less row from another tenant's account is refused the same way: the
// refusal reads nothing of the account.
func TestFireScheduledRefusesOrphanedStepOfAnotherAccount(t *testing.T) {
	exec := orphanStep("scheduled")
	other := "99999999-0000-0000-0000-000000000001"
	exec.CloudAccountID = &other
	mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
	saved := captureSaves(store)
	scheduledDue(store, exec)

	_, err := mgr.FireScheduledDelayedPurchases(context.Background())

	require.NoError(t, err)
	requirePlanGoneRefusal(t, rec, saved)
}

// A plan id whose plan row is missing is refused by executePurchase (no FK in a
// restored or partially migrated store); pinned here so the case stays covered.
func TestFireScheduledRefusesStepWhosePlanRowIsMissing(t *testing.T) {
	exec := armedExecution("azure", "savingsplans", 0, "scheduled")
	exec.Source = ""
	mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
	allowFreshAzurePricing(store, exec)
	saved := captureSaves(store)
	store.GetPurchasePlanFn = func(context.Context, string) (*config.PurchasePlan, error) { return nil, nil }
	scheduledDue(store, exec)

	_, err := mgr.FireScheduledDelayedPurchases(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 0, rec.count())
	assert.Equal(t, config.StatusFailed, saved.last().Status)
	assert.Contains(t, saved.last().Error, "plan not found")
}

// Negative controls: the guard must be as narrow as the orphan.

func TestFireScheduledStillFiresStepOfLivePlan(t *testing.T) {
	exec := armedExecution("azure", "savingsplans", 0, "scheduled")
	exec.Source = ""
	mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
	allowFreshAzurePricing(store, exec)
	scheduledDue(store, exec)

	result, err := mgr.FireScheduledDelayedPurchases(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, rec.count(), "a step of a live plan must still buy")
	assert.Equal(t, 1, result.Fired)
}

func TestFireScheduledStillFiresAdHocPlanlessExecutions(t *testing.T) {
	user := "11111111-2222-3333-4444-555555555555"
	skip := "direct-execute permission"
	cases := map[string]func(*config.PurchaseExecution){
		"plain ad-hoc":             func(e *config.PurchaseExecution) {},
		"web direct execute":       func(e *config.PurchaseExecution) { e.Source = common.PurchaseSourceWeb },
		"direct execute by a user": func(e *config.PurchaseExecution) { e.ExecutedByUserID = &user },
		"skipped approval":         func(e *config.PurchaseExecution) { e.PreApprovalSkipReason = &skip },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			exec := orphanStep("scheduled")
			exec.StepNumber = 0 // direct-execute rows store no step
			mutate(exec)
			mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
			allowFreshAzurePricing(store, exec)
			scheduledDue(store, exec)

			result, err := mgr.FireScheduledDelayedPurchases(context.Background())

			require.NoError(t, err)
			assert.Equal(t, 1, rec.count(), "an ad-hoc plan-less execution must still buy")
			assert.Equal(t, 1, result.Fired)
		})
	}
}

// Run-now and direct execute stamp ExecutedByUserID and PreApprovalSkipReason on
// the claimed row in memory before the executor runs, so an orphan reaching the
// guard through them carries those marks and must still be refused.
func TestRunNowAndDirectExecuteRefuseOrphanedStep(t *testing.T) {
	user := "11111111-2222-3333-4444-555555555555"
	cases := map[string]struct {
		from []string
		run  func(*Manager, string) (*config.PurchaseExecution, string, error)
	}{
		"run now": {[]string{"pending", "notified", "paused"}, func(m *Manager, id string) (*config.PurchaseExecution, string, error) {
			return m.RunPlannedPurchaseNow(context.Background(), id, "operator@example.com", &user, nil)
		}},
		"direct execute": {[]string{"pending", "notified"}, func(m *Manager, id string) (*config.PurchaseExecution, string, error) {
			return m.DirectExecute(context.Background(), id, "operator@example.com", &user, nil)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			exec := orphanStep("paused")
			mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
			saved := captureSaves(store)
			store.On("GetExecutionByID", mock.Anything, exec.ExecutionID).Return(exec, nil).Maybe()
			claimed := *exec
			claimed.Status = "running"
			store.On("TransitionExecutionStatus", mock.Anything, exec.ExecutionID, tc.from, "running", &user).Return(&claimed, nil).Once()

			_, _, err := tc.run(mgr, exec.ExecutionID)

			require.Error(t, err)
			assert.Contains(t, err.Error(), PlanGoneRefusal)
			requirePlanGoneRefusal(t, rec, saved)
		})
	}
}

// A retry successor copies plan_id and step_number and is web-sourced, so it
// must be refused at fire time as well.
func TestFireScheduledRefusesRetrySuccessorOfOrphanedStep(t *testing.T) {
	exec := orphanStep("scheduled")
	exec.Source = common.PurchaseSourceWeb
	exec.RetryAttemptN = 1
	mgr, store, rec := armedHarness(t, common.ServiceSavingsPlansAll)
	saved := captureSaves(store)
	scheduledDue(store, exec)

	_, err := mgr.FireScheduledDelayedPurchases(context.Background())

	require.NoError(t, err)
	requirePlanGoneRefusal(t, rec, saved)
}

func TestIsOrphanedPlanStep(t *testing.T) {
	cases := []struct {
		name string
		exec config.PurchaseExecution
		want bool
	}{
		{"plan-less with a step", config.PurchaseExecution{StepNumber: 1}, true},
		{"plan-less later step", config.PurchaseExecution{StepNumber: 4}, true},
		{"plan-less without a step (direct, web, ladder)", config.PurchaseExecution{StepNumber: 0, IdempotencyKey: ladderTrancheKey}, false},
		{"live plan step", config.PurchaseExecution{PlanID: "p", StepNumber: 1}, false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, IsOrphanedPlanStep(&tc.exec), tc.name)
	}
}
