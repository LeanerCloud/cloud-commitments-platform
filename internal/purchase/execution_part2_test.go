package purchase

import (
	"context"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestProcessPurchaseRecommendations_GlobalConfigError_FailsInsteadOfDefaulting is the
// issue #694 regression guard for HOLE 1: when GetGlobalConfig returns an error,
// processPurchaseRecommendations must return an error and must NOT proceed with
// an empty OfferingClass that silently defaults to "convertible".
//
// The pre-fix code logged the error and continued, so a transient DB failure
// would cause a standard-configured RI to be purchased as convertible (more
// expensive, and irreversible). This test asserts the function returns the DB
// error and PurchaseCommitment is never called.
func TestProcessPurchaseRecommendations_GlobalConfigError_FailsInsteadOfDefaulting(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockFactory := new(MockProviderFactory)

	dbErr := errors.New("connection refused: GlobalConfig read failed")
	// Register the GetGlobalConfig expectation so the mock routes to testify
	// (rather than the no-op default that returns an empty config).
	mockStore.On("GetGlobalConfig", ctx).Return(nil, dbErr)

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-globalcfg-err",
		PlanID:      "",
		Recommendations: []config.RecommendationRecord{
			{Provider: "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300, Savings: 50, Selected: true},
		},
	}
	plan := &config.PurchasePlan{Name: "Direct purchase"}

	manager := &Manager{
		config:          mockStore,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	_, _, purchaseErrors, _, procErr := manager.processPurchaseRecommendations(ctx, exec, plan, "111111111111", nil, "")

	// The function must return an error derived from the DB failure.
	require.Error(t, procErr, "GlobalConfig load failure must abort processing, not silently default to convertible")
	assert.Contains(t, procErr.Error(), "GlobalConfig", "error must mention GlobalConfig so operators can diagnose the DB issue")

	// purchaseErrors must be nil: no rec-level errors should be emitted
	// because the function must abort before reaching the fan-out.
	assert.Nil(t, purchaseErrors, "no rec-level errors expected — function must abort before the fan-out")

	// PurchaseCommitment must NEVER be called: assert the factory was never
	// invoked, which is the clearest proxy for "no cloud API call happened".
	mockFactory.AssertNotCalled(t, "CreateAndValidateProvider", mock.Anything, mock.Anything, mock.Anything)

	mockStore.AssertExpectations(t)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
}
