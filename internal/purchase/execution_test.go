package purchase

import (
	"bytes"
	"context"
	"errors"
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
