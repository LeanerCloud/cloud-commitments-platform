package purchase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// hasPerRecDeadline returns a matcher that asserts the ctx carries a deadline
// whose remaining time is positive and within max. Used by tests that need to
// lock down the per-rec context.WithTimeout(ctx, max) contract introduced for
// issue #683: a bare _, ok := c.Deadline(); return ok matcher would pass even
// if executeSinglePurchase silently fell back to the parent's longer deadline.
//
//nolint:unparam // test helper: max fixed to the per-rec timeout under test
func hasPerRecDeadline(max time.Duration) func(context.Context) bool {
	return func(c context.Context) bool {
		deadline, ok := c.Deadline()
		if !ok {
			return false
		}
		remaining := time.Until(deadline)
		return remaining > 0 && remaining <= max
	}
}

func TestManager_ExecutePurchase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cost   *float64
		logged string
	}{
		{"unknown", nil, "cost=unknown"},
		{"zero", new(float64), "cost=0.00"},
		{"known", new(float64(125.5)), "cost=125.50"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			previousOutput := logging.SetOutput(&logs)
			defer logging.SetOutput(previousOutput)
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)
			mockSTS := new(MockSTSClient)
			mockFactory := new(MockProviderFactory)
			mockProviderInst := new(MockProvider)
			mockServiceClient := new(MockServiceClient)
			assertMocksOnCleanup(t, mockProviderInst, mockServiceClient)

			plan := &config.PurchasePlan{
				ID:   "plan-123",
				Name: "Test Plan",
			}

			exec := &config.PurchaseExecution{
				ExecutionID: "exec-123",
				PlanID:      "plan-123",
				StepNumber:  1,
				Recommendations: []config.RecommendationRecord{
					{
						Provider:     "aws",
						Service:      "ec2",
						ResourceType: "m5.large",
						Region:       "us-east-1",
						Count:        5,
						Savings:      100.0,
						UpfrontCost:  500.0,
						Selected:     true,
					},
					{
						Provider:     "aws",
						Service:      "rds",
						ResourceType: "db.r5.large",
						Region:       "us-west-2",
						Count:        2,
						Savings:      50.0,
						UpfrontCost:  200.0,
						Selected:     false, // Not selected
					},
				},
			}

			mockStore.On("GetPurchasePlan", ctx, "plan-123").Return(plan, nil)
			mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Run(func(args mock.Arguments) {
				assert.Equal(t, tc.cost, args.Get(1).(*config.PurchaseHistoryRecord).UpfrontCost)
			}).Return(nil)
			mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
			mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
				Account: aws.String("123456789012"),
			}, nil)

			// Mock provider factory to return a mock provider
			mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
			mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
			mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
				Success:      true,
				CommitmentID: "ri-12345",
				Cost:         tc.cost,
			}, nil)

			manager := &Manager{
				config:          mockStore,
				email:           mockEmail,
				stsClient:       mockSTS,
				providerFactory: mockFactory,
				dashboardURL:    "https://dashboard.example.com",
			}

			err := manager.executePurchase(ctx, exec)
			require.NoError(t, err)

			// Verify that only selected recommendation was purchased
			assert.True(t, exec.Recommendations[0].Purchased)
			assert.NotEmpty(t, exec.Recommendations[0].PurchaseID)
			assert.False(t, exec.Recommendations[1].Purchased)

			mockStore.AssertExpectations(t)
			mockEmail.AssertExpectations(t)
			mockSTS.AssertExpectations(t)
			mockFactory.AssertExpectations(t)
			assert.Contains(t, logs.String(), tc.logged)
			assert.NotContains(t, logs.String(), "%!")
		})
	}
}

// TestManager_ExecutePurchase_WebSourcePropagates covers the gap noted in the
// plan audit: the CLI path has a source-asserting test via TestExecutePurchase,
// but the web path (web handler → DB → Manager.executePurchase → provider)
// needs its own check that exec.Source ends up as opts.Source on the
// provider's PurchaseCommitment call.
func TestManager_ExecutePurchase_WebSourcePropagates(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockSTS := new(MockSTSClient)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	assertMocksOnCleanup(t, mockStore, mockEmail, mockSTS, mockFactory, mockProviderInst)
	mockServiceClient := new(MockServiceClient)

	plan := &config.PurchasePlan{ID: "plan-web", Name: "Web Plan"}
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-web",
		PlanID:      "plan-web",
		StepNumber:  1,
		Source:      common.PurchaseSourceWeb,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, Selected: true},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-web").Return(plan, nil)
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)
	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	// The deterministic idempotency token (issue #636) is derived per-rec from
	// the execution ID and the rec's index, so the web path now also carries it.
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.AnythingOfType("common.Recommendation"),
		optsIgnoringRequestedAt(common.PurchaseOptions{
			Source:           common.PurchaseSourceWeb,
			IdempotencyToken: common.DeriveIdempotencyToken("exec-web", 0),
			ExecutionID:      "exec-web",
		}),
	).Return(common.PurchaseResult{Success: true, CommitmentID: "ri-web"}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.NoError(t, err)
	mockServiceClient.AssertExpectations(t)
}

// TestManager_ExecutePurchase_InvalidSourceFallsBackUntagged verifies the
// NormalizeSource defense-in-depth: a DB row with an unexpected source value
// proceeds with an empty source (untagged) rather than failing the already-
// approved execution or poisoning cloud tags with arbitrary strings.
func TestManager_ExecutePurchase_InvalidSourceFallsBackUntagged(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockSTS := new(MockSTSClient)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	assertMocksOnCleanup(t, mockStore, mockEmail, mockSTS, mockFactory, mockProviderInst)
	mockServiceClient := new(MockServiceClient)

	plan := &config.PurchasePlan{ID: "plan-bad", Name: "Bad Plan"}
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-bad",
		PlanID:      "plan-bad",
		StepNumber:  1,
		Source:      "cudly-evil", // not in whitelist — should be rejected
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, Selected: true},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-bad").Return(plan, nil)
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("123456789012"),
	}, nil)
	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil)
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	// Expect EMPTY source, not "cudly-evil" — NormalizeSource must have wiped it.
	// The idempotency token (issue #636) is still derived regardless of source.
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.AnythingOfType("common.Recommendation"),
		optsIgnoringRequestedAt(common.PurchaseOptions{
			Source:           "",
			IdempotencyToken: common.DeriveIdempotencyToken("exec-bad", 0),
			ExecutionID:      "exec-bad",
		}),
	).Return(common.PurchaseResult{Success: true, CommitmentID: "ri-bad"}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.NoError(t, err)
	mockServiceClient.AssertExpectations(t)
}

func TestManager_ExecutePurchase_PlanNotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-123",
		PlanID:      "nonexistent",
		StepNumber:  1,
	}

	mockStore.On("GetPurchasePlan", ctx, "nonexistent").Return(nil, nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "plan not found")

	mockStore.AssertExpectations(t)
}

func TestManager_ExecutePurchase_GetPlanError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-123",
		PlanID:      "plan-123",
		StepNumber:  1,
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-123").Return(nil, errors.New("database error"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get plan")

	mockStore.AssertExpectations(t)
}

// A plan step with no recommendations buys nothing, so it fails instead of
// completing and advancing the ramp (#609).
func TestManager_ExecutePurchase_NoRecommendations(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	plan := &config.PurchasePlan{
		ID:   "plan-123",
		Name: "Test Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID:     "exec-123",
		PlanID:          "plan-123",
		StepNumber:      1,
		Recommendations: []config.RecommendationRecord{},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-123").Return(plan, nil)

	manager := &Manager{
		config:       mockStore,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	require.ErrorIs(t, err, ErrPlanStepNoRecommendations)
}

func TestManager_UpdatePlanProgress(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("CompletePlanStep", ctx, "plan-123", 2).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.updatePlanProgress(ctx, &config.PurchaseExecution{
		ExecutionID: "exec-123",
		PlanID:      "plan-123",
		StepNumber:  2,
	})
	require.NoError(t, err)

	mockStore.AssertExpectations(t)
}

func TestManager_UpdatePlanProgress_PlanNotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	// CompletePlanStep returns nil when the plan no longer exists.
	mockStore.On("CompletePlanStep", ctx, "nonexistent", 1).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.updatePlanProgress(ctx, &config.PurchaseExecution{
		ExecutionID: "exec-nonexistent",
		PlanID:      "nonexistent",
		StepNumber:  1,
	})
	require.NoError(t, err)

	mockStore.AssertExpectations(t)
}

func TestManager_UpdatePlanProgress_GetError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	mockStore.On("CompletePlanStep", ctx, "plan-123", 1).Return(errors.New("database error"))

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.updatePlanProgress(ctx, &config.PurchaseExecution{
		ExecutionID: "exec-123",
		PlanID:      "plan-123",
		StepNumber:  1,
	})
	assert.Error(t, err)

	mockStore.AssertExpectations(t)
}

func TestManager_UpdatePlanProgress_CompleteRamp(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	// The step-completion and ramp-advance logic now live inside CompletePlanStep
	// in the store, tested at the store layer with pgxmock.
	mockStore.On("CompletePlanStep", ctx, "plan-123", 4).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.updatePlanProgress(ctx, &config.PurchaseExecution{
		ExecutionID: "exec-123",
		PlanID:      "plan-123",
		StepNumber:  4,
	})
	require.NoError(t, err)

	mockStore.AssertExpectations(t)
}

// TestManager_UpdatePlanProgress_ZeroStepRefusesAndSkipsStore is the
// regression guard for issue #1669: an execution attributed to a plan but
// carrying a non-positive ramp step must not guess at the ramp position by
// calling the store. updatePlanProgress must return an error and the store
// must never be called.
func TestManager_UpdatePlanProgress_ZeroStepRefusesAndSkipsStore(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	// Registered so AssertNotCalled below is a meaningful check rather than a
	// vacuous one (a bare AssertNotCalled without a matching registered
	// expectation always passes in this repo's mock setup).
	mockStore.On("CompletePlanStep", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	err := manager.updatePlanProgress(ctx, &config.PurchaseExecution{
		ExecutionID: "exec-zero-step",
		PlanID:      "plan-123",
		StepNumber:  0,
	})
	require.Error(t, err)

	mockStore.AssertNotCalled(t, "CompletePlanStep", mock.Anything, mock.Anything, mock.Anything)
}

// TestExecuteAndFinalize_RefusedRampAdvanceIsRecordedOnTheRow is the CR
// follow-up guard on #1862: the refusal paths are new in issue #1669 (the old
// blind CurrentStep++ could not decline), so money can now be spent on a step
// the plan then declines to count. Before this, the only trace was a log line,
// and a stalled plan is not self-correcting because shouldNotifyPlan reads the
// stale next_execution_date as daysUntil < 0 and stops notifying.
//
// The execution must stay "completed" (the purchase did complete) while the
// refusal is persisted on the row so History shows it.
func TestExecuteAndFinalize_RefusedRampAdvanceIsRecordedOnTheRow(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	// A plan-attributed row carrying no ramp step: the deploy-overlap shape that
	// updatePlanProgress refuses. CompletePlanStep is registered but must never
	// be reached, so the refusal comes from the caller rather than the store.
	exec := &config.PurchaseExecution{
		ExecutionID: "exec-refused-advance",
		PlanID:      "plan-refuse",
		Status:      "running",
		StepNumber:  0,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300, Selected: true},
		},
	}

	var saves []config.PurchaseExecution
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		saves = append(saves, *e)
		return nil
	}
	mockStore.GetPurchasePlanFn = func(_ context.Context, id string) (*config.PurchasePlan, error) {
		return &config.PurchasePlan{ID: id, Name: "Refuse Plan"}, nil
	}
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return nil, nil
	}
	mockStore.On("CompletePlanStep", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Maybe()
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil).Maybe()
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProviderInst, nil).Maybe()
	mockProviderInst.On("GetServiceClient", mock.Anything, common.ServiceEC2, mock.Anything).Return(mockServiceClient, nil).Maybe()
	mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-ok"}, nil).Maybe()

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       awsAccessKeyCredStore(),
		dashboardURL:    "https://dashboard.example.com",
	}

	require.NoError(t, manager.executeAndFinalize(ctx, exec),
		"a refused ramp advance must not fail the purchase; the money already moved")

	// Assert a non-zero number of saves before asserting anything about their
	// content, so an empty slice cannot satisfy the checks below.
	require.GreaterOrEqual(t, len(saves), 2,
		"the terminal save plus the ramp-refusal save must both reach the store")

	final := saves[len(saves)-1]
	assert.Equal(t, "completed", final.Status,
		"the purchase completed; only the plan's progress accounting did not")
	assert.Contains(t, final.Error, "ramp not advanced",
		"the refusal must survive on the row, not only in the log")
	assert.Contains(t, final.Error, "plan-refuse",
		"the recorded note must name the plan whose ramp stalled")

	mockStore.AssertNotCalled(t, "CompletePlanStep", mock.Anything, mock.Anything, mock.Anything)
}

// runRampAdvanceOutcome drives executeAndFinalize for a plan-attributed
// execution whose ramp advance the store declines with declineErr, and returns
// the last execution row that reached the store.
//
// Shared by the cases below because what separates them is only which sentinel
// CompletePlanStep returns; duplicating the whole fan-out fixture per case would
// bury that one line.
func runRampAdvanceOutcome(t *testing.T, execID string, declineErr error) config.PurchaseExecution {
	t.Helper()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })

	exec := &config.PurchaseExecution{
		ExecutionID: execID,
		PlanID:      "plan-multi",
		Status:      "running",
		StepNumber:  3,
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300, Selected: true},
		},
	}

	var saves []config.PurchaseExecution
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		saves = append(saves, *e)
		return nil
	}
	mockStore.GetPurchasePlanFn = func(_ context.Context, id string) (*config.PurchasePlan, error) {
		return &config.PurchasePlan{ID: id, Name: "Multi Plan"}, nil
	}
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return nil, nil
	}
	mockStore.On("CompletePlanStep", mock.Anything, "plan-multi", 3).Return(declineErr)
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Maybe()
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil).Maybe()
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProviderInst, nil).Maybe()
	mockProviderInst.On("GetServiceClient", mock.Anything, common.ServiceEC2, mock.Anything).Return(mockServiceClient, nil).Maybe()
	mockServiceClient.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-ok"}, nil).Maybe()

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       awsAccessKeyCredStore(),
		dashboardURL:    "https://dashboard.example.com",
	}

	require.NoError(t, manager.executeAndFinalize(ctx, exec),
		"a declined ramp advance must never fail the purchase that just completed")
	require.NotEmpty(t, saves, "the terminal save must reach the store")
	mockStore.AssertCalled(t, "CompletePlanStep", mock.Anything, "plan-multi", 3)
	return saves[len(saves)-1]
}

// TestExecuteAndFinalize_IncompleteRampStepIsNotStampedOnTheRow is the
// counterpart guard for issue #1861: a step still waiting on its other accounts
// is the ordinary shape of a multi-account fan-out being repaired one account
// at a time, and it stops being true the moment the last account buys. Stamping
// it would leave a permanent note describing a ramp that has since advanced,
// and would flip a cleanly-completed row into History's audit-gap rendering,
// which keys on a non-empty Error. Plan health derives that state live instead
// (config.GetStuckRampSteps).
func TestExecuteAndFinalize_IncompleteRampStepIsNotStampedOnTheRow(t *testing.T) {
	final := runRampAdvanceOutcome(t, "exec-incomplete-step",
		fmt.Errorf("%w: 1 account(s) of plan plan-multi ramp step 3 have not bought", config.ErrRampStepIncomplete))

	assert.Equal(t, "completed", final.Status)
	assert.NotContains(t, final.Error, "ramp not advanced",
		"a transient wait must leave no note that will outlive it")
}

// TestExecuteAndFinalize_SiblingCountedStepIsNotStampedOnTheRow closes the
// second half of the same hazard. When the last two accounts of a step finish
// together, one advances the ramp and the other is told the step is already
// counted -- routine, and the step WAS counted, so nothing is wrong with that
// purchase. Stamping it would put "ramp not advanced" on a clean row and make
// History announce that the purchase's history record could not be saved.
func TestExecuteAndFinalize_SiblingCountedStepIsNotStampedOnTheRow(t *testing.T) {
	final := runRampAdvanceOutcome(t, "exec-sibling-counted",
		fmt.Errorf("%w: plan plan-multi is already on ramp step 3", config.ErrRampStepCountedBySibling))

	assert.Equal(t, "completed", final.Status)
	assert.NotContains(t, final.Error, "ramp not advanced",
		"losing a race to a sibling of the same step is not an anomaly worth recording")
}

// TestExecuteAndFinalize_PassedStepIsStampedOnTheRow is the positive control for
// the two above: the discrimination has to keep recording the case issue #1861
// actually asks for. A row completing a step the ramp moved PAST bought
// commitment the plan counted earlier and will never count again, and that is
// invisible everywhere else once the Lambda log ages out.
func TestExecuteAndFinalize_PassedStepIsStampedOnTheRow(t *testing.T) {
	final := runRampAdvanceOutcome(t, "exec-passed-step",
		fmt.Errorf("%w: plan plan-multi is on ramp step 5, past the completing step 3", config.ErrRampStepAlreadyCounted))

	assert.Equal(t, "completed", final.Status,
		"the purchase completed; only the plan's progress accounting did not")
	assert.Contains(t, final.Error, "ramp not advanced",
		"a step the ramp has passed must survive on the row, not only in the log")
	assert.Contains(t, final.Error, "past the completing step 3")
}

func TestManager_GetAWSAccountID_Success(t *testing.T) {
	ctx := context.Background()
	mockSTS := new(MockSTSClient)

	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: aws.String("987654321098"),
	}, nil)

	manager := &Manager{
		stsClient: mockSTS,
	}

	accountID, err := manager.getAWSAccountID(ctx)
	require.NoError(t, err)
	assert.Equal(t, "987654321098", accountID)

	mockSTS.AssertExpectations(t)
}

func TestManager_GetAWSAccountID_NoClient(t *testing.T) {
	ctx := context.Background()

	manager := &Manager{
		stsClient: nil, // No STS client configured
	}

	_, err := manager.getAWSAccountID(ctx)
	require.Error(t, err, "nil STS client must return an error (not the 'unknown' sentinel)")
}

func TestManager_GetAWSAccountID_Error(t *testing.T) {
	ctx := context.Background()
	mockSTS := new(MockSTSClient)

	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(nil, errors.New("STS error"))

	manager := &Manager{
		stsClient: mockSTS,
	}

	_, err := manager.getAWSAccountID(ctx)
	require.Error(t, err, "STS failure must return an error")

	mockSTS.AssertExpectations(t)
}

func TestManager_GetAWSAccountID_NilAccount(t *testing.T) {
	ctx := context.Background()
	mockSTS := new(MockSTSClient)

	mockSTS.On("GetCallerIdentity", ctx, mock.AnythingOfType("*sts.GetCallerIdentityInput")).Return(&sts.GetCallerIdentityOutput{
		Account: nil, // Nil account in response
	}, nil)

	manager := &Manager{
		stsClient: mockSTS,
	}

	_, err := manager.getAWSAccountID(ctx)
	require.Error(t, err, "nil Account in STS response must return an error")

	mockSTS.AssertExpectations(t)
}

func TestManager_ExecutePurchase_MultiAccount(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	assertMocksOnCleanup(t, mockProvider, mockServiceClient)
	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return []byte(`{"access_key_id":"AKIAIOSFODNN7EXAMPLE","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
		},
	}

	plan := &config.PurchasePlan{
		ID:           "plan-multi",
		Name:         "Multi-Account Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}

	rec := config.RecommendationRecord{
		Provider:     "aws",
		Service:      "ec2",
		ResourceType: "m5.large",
		Region:       "us-east-1",
		Count:        1,
		Savings:      75.0,
		UpfrontCost:  300.0,
		Selected:     true,
	}

	exec := &config.PurchaseExecution{
		ExecutionID:     "exec-multi",
		PlanID:          "plan-multi",
		Status:          "pending",
		Recommendations: []config.RecommendationRecord{withAccount(rec, "aaaaaaaa-0000-0000-0000-000000000001"), withAccount(rec, "aaaaaaaa-0000-0000-0000-000000000002")},
	}

	accounts := []config.CloudAccount{
		{ID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "Prod", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: "aaaaaaaa-0000-0000-0000-000000000002", Name: "Staging", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-multi").Return(plan, nil)
	// GetPlanAccounts and SavePurchaseExecution use override fns (not .On) because the
	// goroutine-based fan-out bypasses the testify mock tracker.
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return accounts, nil
	}
	var savedExecs []*config.PurchaseExecution
	var mu sync.Mutex
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, exec *config.PurchaseExecution) error {
		mu.Lock()
		savedExecs = append(savedExecs, exec)
		mu.Unlock()
		return nil
	}
	// SavePurchaseHistory and SendPurchaseConfirmation are called once per account.
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Times(2)
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil).Times(2)

	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(common.PurchaseResult{
		Success: true, CommitmentID: "ri-99999",
	}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
		// assumeRoleSTS is nil → access_keys path, no role assumption needed
	}

	err := manager.executePurchase(ctx, exec)
	require.NoError(t, err)

	// The original exec record should be unchanged (fan-out creates per-account copies).
	assert.Nil(t, exec.CloudAccountID)

	// Two per-account execution records should have been saved, each with a distinct account ID.
	require.Len(t, savedExecs, 2)
	accountIDs := []string{*savedExecs[0].CloudAccountID, *savedExecs[1].CloudAccountID}
	assert.ElementsMatch(t, []string{
		"aaaaaaaa-0000-0000-0000-000000000001",
		"aaaaaaaa-0000-0000-0000-000000000002",
	}, accountIDs)

	// platform#631: each account buys only its own rec (2 calls, not 4) and
	// its row carries its own totals, not the whole step's.
	mockServiceClient.AssertNumberOfCalls(t, "PurchaseCommitment", 2)
	for _, e := range savedExecs {
		require.Len(t, e.Recommendations, 1)
		assert.Equal(t, *e.CloudAccountID, *e.Recommendations[0].CloudAccountID)
	}

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
	mockFactory.AssertExpectations(t)
}

// TestExecuteForAccount_CredentialFailure_MarksFailed locks down the invariant
// that a credential-resolution error is a hard failure: the per-account
// execution record must be saved with Status="failed", the provider factory
// must NEVER be invoked (no ambient fallback), and executePurchase must
// surface the error. The production bug this guards was fixed in 9531681a4
// across AWS/Azure/GCP; this test ensures a future refactor cannot silently
// reintroduce the ambient fallback.
func TestExecuteForAccount_CredentialFailure_MarksFailed(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		account  config.CloudAccount
	}{
		{
			name:     "aws access_keys credentials missing",
			provider: "aws",
			account: config.CloudAccount{
				ID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "aws-failing",
				Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys",
			},
		},
		{
			name:     "azure client_secret credentials missing",
			provider: "azure",
			account: config.CloudAccount{
				ID: "bbbbbbbb-0000-0000-0000-000000000002", Name: "azure-failing",
				Provider: "azure", ExternalID: "sub-222", AzureAuthMode: "client_secret",
				AzureTenantID: "tenant-222", AzureClientID: "client-222",
			},
		},
		{
			name:     "gcp service_account credentials missing",
			provider: "gcp",
			account: config.CloudAccount{
				ID: "cccccccc-0000-0000-0000-000000000003", Name: "gcp-failing",
				Provider: "gcp", ExternalID: "proj-333", GCPAuthMode: "service_account_key",
				GCPProjectID: "proj-333",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			assertMocksOnCleanup(t, mockStore)
			mockEmail := allowFailedNotification(new(MockEmailSender))
			mockFactory := new(MockProviderFactory)
			// Credential store returns nothing for every load — this triggers
			// the "no credentials stored" error path in the resolver for all
			// three providers.
			credStore := &MockCredentialStore{
				LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
					return nil, nil
				},
			}

			plan := &config.PurchasePlan{
				ID:           "plan-credfail",
				Name:         "Credential Failure Plan",
				RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
			}
			rec := config.RecommendationRecord{
				Provider: tt.provider, Service: "ec2", ResourceType: "m5.large",
				Region: "us-east-1", Count: 1, Savings: 75.0, UpfrontCost: 300.0, Selected: true,
			}
			exec := &config.PurchaseExecution{
				ExecutionID:     "exec-credfail",
				PlanID:          "plan-credfail",
				Status:          "pending",
				Recommendations: []config.RecommendationRecord{withAccount(rec, tt.account.ID)},
			}

			mockStore.On("GetPurchasePlan", ctx, "plan-credfail").Return(plan, nil)
			mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
				return []config.CloudAccount{tt.account}, nil
			}
			var saved []*config.PurchaseExecution
			var mu sync.Mutex
			mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
				mu.Lock()
				saved = append(saved, e)
				mu.Unlock()
				return nil
			}

			manager := &Manager{
				config:          mockStore,
				email:           mockEmail,
				providerFactory: mockFactory,
				credStore:       credStore,
			}

			err := manager.executePurchase(ctx, exec)

			// Must surface the credential failure — no ambient fallback.
			require.Error(t, err)
			assert.Contains(t, err.Error(), "credential resolution failed")

			// Per-account execution record must be persisted with Status=failed
			// so the audit trail reflects the failure.
			require.NotEmpty(t, saved, "SavePurchaseExecution should be called for the failed account")
			assert.Equal(t, "failed", saved[0].Status)
			assert.NotEmpty(t, saved[0].Error, "failed execution record must carry the error message")

			// The provider factory must NEVER be invoked when credentials
			// fail to resolve. If this fires, something is attempting an
			// ambient-credential fallback — exactly the bug 9531681a4 closed.
			mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// TestExecuteForAccount_CredentialFailure_SaveErrorSurfaced is the regression
// guard for COR-10 (#1184): when credential resolution fails AND persisting
// the per-account "failed" row also fails, the save failure must surface as an
// AUDIT LOSS error alongside the credential error instead of being silently
// discarded. The failed row is as much audit trail as a success row.
func TestExecuteForAccount_CredentialFailure_SaveErrorSurfaced(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockFactory := new(MockProviderFactory)

	// No credentials stored — credential resolution fails for the account.
	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, _, _ string) ([]byte, error) {
			return nil, nil
		},
	}

	// Persisting the per-account "failed" row also fails.
	saveErr := errors.New("db write failed")
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, _ *config.PurchaseExecution) error {
		return saveErr
	}

	account := config.CloudAccount{
		ID: "aaaaaaaa-0000-0000-0000-000000000001", Name: "aws-failing",
		Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys",
	}
	plan := &config.PurchasePlan{
		ID:           "plan-credfail-save",
		Name:         "Credential Failure Save Error Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}
	baseExec := &config.PurchaseExecution{
		ExecutionID: "exec-credfail-save",
		PlanID:      "plan-credfail-save",
		Status:      "pending",
		Recommendations: []config.RecommendationRecord{
			{
				Provider: "aws", Service: "ec2", ResourceType: "m5.large",
				Region: "us-east-1", Count: 1, Selected: true,
			},
		},
	}

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
	}

	committed, err := manager.executeForAccount(ctx, baseExec, plan, account)
	assert.False(t, committed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AUDIT LOSS: failed to save execution record for account "+account.ID)
	assert.ErrorIs(t, err, saveErr, "the underlying save error must stay inspectable via errors.Is")
	assert.Contains(t, err.Error(), "credential resolution failed for account "+account.ID,
		"the original credential failure must not be masked by the save failure")

	// No provider may ever be constructed when credentials fail to resolve.
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)
	mockStore.AssertExpectations(t)
}

// TestExecuteMultiAccount_PartialFailure_IsolatesAccounts is the regression
// guard for spec E-2: when one account (account-I) has invalid credentials and
// another (account-V) has valid credentials, account-V's execution must
// complete successfully and its record must be independent of account-I's failure.
//
// What this test pins:
//   - Two SavePurchaseExecution calls fire (one per account — no record is skipped).
//   - account-V's record: Status=="completed", PurchaseID set (purchase went through).
//   - account-I's record: Status=="failed", Error non-empty.
//   - account-I's error text does NOT contain account-V's credential material
//     (cross-account data isolation / log-sanitisation guard).
//   - account-V's CommitmentID does NOT appear in account-I's error text
//     (result_data independence).
//   - The overall executePurchase call returns an error (aggregated from I) —
//     proving the errgroup collected I's failure — while V's record was still
//     saved as "completed" (proving the errgroup did NOT short-circuit on I).
func TestExecuteMultiAccount_PartialFailure_IsolatesAccounts(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockFactory := new(MockProviderFactory)
	mockProviderInst := new(MockProvider)
	mockServiceClient := new(MockServiceClient)

	// account-V: valid mock credentials (access_keys path, LoadRaw returns key JSON).
	// account-I: invalid mock credentials (LoadRaw returns nil → "no credentials" resolver error).
	const (
		accountVID  = "vvvvvvvv-0000-0000-0000-000000000001"
		accountIID  = "iiiiiiii-0000-0000-0000-000000000002"
		accountVKey = "AKIAIOSFODNN7EXAMPLE" // sentinel — must not appear in I's error
		commitmentV = "ri-valid-001"         // V's CommitmentID — must not appear in I's error
	)

	accounts := []config.CloudAccount{
		{ID: accountVID, Name: "Valid", Provider: "aws", ExternalID: "111111111111", AWSAuthMode: "access_keys"},
		{ID: accountIID, Name: "Invalid", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "access_keys"},
	}

	rec := config.RecommendationRecord{
		Provider:     "aws",
		Service:      "ec2",
		ResourceType: "m5.large",
		Region:       "us-east-1",
		Count:        1,
		Savings:      75.0,
		UpfrontCost:  300.0,
		Selected:     true,
	}

	exec := &config.PurchaseExecution{
		ExecutionID:     "exec-partial",
		PlanID:          "plan-partial",
		Status:          "pending",
		Recommendations: []config.RecommendationRecord{withAccount(rec, accountVID), withAccount(rec, accountIID)},
	}

	plan := &config.PurchasePlan{
		ID:           "plan-partial",
		Name:         "Partial Failure Plan",
		RampSchedule: config.RampSchedule{CurrentStep: 0, TotalSteps: 1},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-partial").Return(plan, nil)
	mockStore.GetPlanAccountsFn = func(_ context.Context, _ string) ([]config.CloudAccount, error) {
		return accounts, nil
	}

	// Collect saved execution records concurrency-safely (goroutine fan-out).
	var savedExecs []*config.PurchaseExecution
	var mu sync.Mutex
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		mu.Lock()
		// Copy the record before appending — executeForAccount reuses the acctExec
		// struct value but Recommendations slice is already deep-copied; capture
		// the status and error which are set before Save is called.
		saved := *e
		savedExecs = append(savedExecs, &saved)
		mu.Unlock()
		return nil
	}

	// Only account-V reaches the provider; account-I fails at credential resolution.
	// Use .Once() so testify fails if the factory is called for account-I as well.
	mockFactory.On("CreateAndValidateProvider", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), "aws", mock.Anything).Return(mockProviderInst, nil).Once()
	mockProviderInst.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceEC2, "us-east-1").Return(mockServiceClient, nil).Once()
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
		mock.AnythingOfType("common.Recommendation"),
		mock.AnythingOfType("common.PurchaseOptions"),
	).Return(common.PurchaseResult{Success: true, CommitmentID: commitmentV}, nil).Once()

	// Only account-V triggers history + notification.
	mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil).Once()
	mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil).Once()

	// Credential store: V gets valid key JSON; I gets nil (no credentials stored).
	credStore := &MockCredentialStore{
		LoadRawFn: func(_ context.Context, accountID, _ string) ([]byte, error) {
			if accountID == accountVID {
				return []byte(`{"access_key_id":"` + accountVKey + `","secret_access_key":"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}`), nil
			}
			// account-I: no credentials stored → resolver returns error.
			return nil, nil
		},
	}

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		credStore:       credStore,
	}

	err := manager.executePurchase(ctx, exec)

	// The call must return an error (account-I's failure is aggregated).
	// This proves the errgroup collected the failure rather than discarding it.
	require.Error(t, err, "executePurchase must surface account-I's credential failure")

	// Both per-account records must have been saved (no silent skip).
	require.Len(t, savedExecs, 2, "exactly two SavePurchaseExecution calls expected — one per account")

	// Build a map keyed by cloud_account_id for order-independent lookup.
	byAccount := make(map[string]*config.PurchaseExecution, 2)
	for _, e := range savedExecs {
		require.NotNil(t, e.CloudAccountID, "every saved record must have cloud_account_id set")
		byAccount[*e.CloudAccountID] = e
	}

	// --- account-V assertions (success path must be unaffected by I's failure) ---
	recordV, ok := byAccount[accountVID]
	require.True(t, ok, "account-V's execution record must be saved")
	assert.Equal(t, "completed", recordV.Status, "account-V must complete successfully")
	assert.Empty(t, recordV.Error, "account-V's record must carry no error message")

	// Verify the purchase went through: at least one recommendation must be marked Purchased.
	purchasedV := false
	for _, r := range recordV.Recommendations {
		if r.Purchased {
			purchasedV = true
			assert.Equal(t, commitmentV, r.PurchaseID, "account-V's commitment ID must match the mock return value")
		}
	}
	assert.True(t, purchasedV, "account-V must have at least one purchased recommendation")

	// --- account-I assertions (failure path must be correctly recorded) ---
	recordI, ok := byAccount[accountIID]
	require.True(t, ok, "account-I's execution record must be saved")
	assert.Equal(t, "failed", recordI.Status, "account-I must be marked failed")
	assert.NotEmpty(t, recordI.Error, "account-I's record must carry an error message")

	// Log-sanitisation guard: account-I's error must not contain account-V's
	// credential material (cross-account data leak would be a security regression).
	assert.NotContains(t, recordI.Error, accountVKey,
		"account-I's error must not contain account-V's access key (cross-account credential leak)")

	// Result-data independence: account-V's CommitmentID must not bleed into
	// account-I's error or recommendation error fields.
	assert.NotContains(t, recordI.Error, commitmentV,
		"account-I's error must not reference account-V's commitment ID")
	for _, r := range recordI.Recommendations {
		assert.NotContains(t, r.Error, commitmentV,
			"account-I's recommendation errors must not reference account-V's commitment ID")
	}

	// The base exec record must remain untagged (fan-out creates per-account copies).
	assert.Nil(t, exec.CloudAccountID, "base execution record must have nil cloud_account_id after fan-out")

	mockStore.AssertExpectations(t)
	mockEmail.AssertExpectations(t)
	mockFactory.AssertExpectations(t)
	mockProviderInst.AssertExpectations(t)
	mockServiceClient.AssertExpectations(t)
}
