package purchase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/credentials"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestApplyEngineFallback covers the engine-backfill helper directly so a
// future refactor that changes which *Details types carry an Engine field
// surfaces here rather than via a silent mis-purchase regression.
func TestApplyEngineFallback(t *testing.T) {
	// RDS: empty Engine should be backfilled.
	rds := &common.DatabaseDetails{}
	applyEngineFallback(rds, "mysql")
	assert.Equal(t, "mysql", rds.Engine, "empty DB Engine must be backfilled from the record column")

	// RDS: pre-populated Engine must NOT be overwritten — Details is
	// the source of truth when present.
	rdsKeep := &common.DatabaseDetails{Engine: "postgres"}
	applyEngineFallback(rdsKeep, "mysql")
	assert.Equal(t, "postgres", rdsKeep.Engine, "non-empty DB Engine must be preserved")

	// Cache: same rule.
	ec := &common.CacheDetails{}
	applyEngineFallback(ec, "redis")
	assert.Equal(t, "redis", ec.Engine)

	// Empty engine arg is a no-op.
	rdsZero := &common.DatabaseDetails{}
	applyEngineFallback(rdsZero, "")
	assert.Empty(t, rdsZero.Engine, "empty engine arg must leave Details untouched")

	// Non-DB/Cache types are no-ops (don't accidentally write into them).
	cc := &common.ComputeDetails{}
	applyEngineFallback(cc, "mysql")
	assert.Empty(t, cc.Platform, "ComputeDetails must remain untouched by applyEngineFallback")
}

// The stored per-account execution error is visible to users scoped to the
// tenant account, so a failed host-identity check must not expose the host
// account id or the raw STS error.
func TestExecuteForAccount_NonHostRoleARN_StoredErrorOmitsHostIdentity(t *testing.T) {
	ctx := context.Background()
	const hostID = "111111111111"
	hostSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, hostSTS)
	hostSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(&sts.GetCallerIdentityOutput{Account: aws.String(hostID)}, nil)

	var saved []*config.PurchaseExecution
	mockStore := new(MockConfigStore)
	mockStore.SavePurchaseExecutionFn = func(_ context.Context, e *config.PurchaseExecution) error {
		saved = append(saved, e)
		return nil
	}
	m := &Manager{
		config:          mockStore,
		assumeRoleSTS:   new(MockAssumeRoleSTS),
		stsClient:       hostSTS,
		ambientAWSCreds: aws.AnonymousCredentials{},
	}
	account := config.CloudAccount{ID: "tenant", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "role_arn"}
	baseExec := &config.PurchaseExecution{
		ExecutionID:     "exec-nonhost",
		PlanID:          "plan-nonhost",
		Recommendations: []config.RecommendationRecord{{Provider: "aws", Service: "ec2", Count: 1, Selected: true}},
	}

	committed, err := m.executeForAccount(ctx, baseExec, &config.PurchasePlan{ID: "plan-nonhost"}, account)
	require.ErrorIs(t, err, credentials.ErrNotHostAccount)
	assert.False(t, committed)
	require.NotEmpty(t, saved)
	for _, e := range saved {
		assert.NotContains(t, e.Error, hostID)
		assert.NotContains(t, e.Error, "GetCallerIdentity")
	}
	assert.Contains(t, saved[len(saved)-1].Error, credentials.ErrNotHostAccount.Error())
}

// TestManager_ExecuteSinglePurchase_LegacyEmptyDetails locks down the
// graceful-degradation path for rows persisted before #453 — they carry
// an empty Details payload but the cloud client's findOfferingID still
// needs a typed *pointer* to type-assert against. DecodeServiceDetailsFor
// returns a zero-valued typed pointer in that case; the engine column
// (which legacy rows DID carry) is folded back onto the Details by
// applyEngineFallback so a non-default-engine DB rec doesn't silently
// mis-purchase as the default.
func TestManager_ExecuteSinglePurchase_LegacyEmptyDetails(t *testing.T) {
	cases := []struct {
		assertDetails func(t *testing.T, d common.ServiceDetails)
		name          string
		service       string
		serviceType   common.ServiceType
		region        string
		resource      string
		engine        string
	}{
		{
			name:        "legacy_ec2",
			service:     "ec2",
			serviceType: common.ServiceEC2,
			region:      "us-east-1",
			resource:    "t4g.nano",
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				cd, ok := d.(*common.ComputeDetails)
				if assert.True(t, ok, "EC2 legacy details must be *common.ComputeDetails, got %T", d) {
					// Zero-valued — service client's
					// buildOfferingFilters substitutes defaults.
					assert.Empty(t, cd.Platform)
					assert.Empty(t, cd.Tenancy)
				}
			},
		},
		{
			name:        "legacy_rds_postgres",
			service:     "rds",
			serviceType: common.ServiceRDS,
			region:      "us-east-1",
			resource:    "db.r5.large",
			engine:      "postgres",
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				dd, ok := d.(*common.DatabaseDetails)
				if assert.True(t, ok, "RDS legacy details must be *common.DatabaseDetails, got %T", d) {
					// Engine must be backfilled from the Engine
					// column — without applyEngineFallback, the
					// Postgres rec would silently mis-purchase as
					// MySQL (or whatever default).
					assert.Equal(t, "postgres", dd.Engine)
				}
			},
		},
		{
			name:        "legacy_elasticache_redis",
			service:     "elasticache",
			serviceType: common.ServiceElastiCache,
			region:      "us-east-1",
			resource:    "cache.r5.large",
			engine:      "redis",
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				cd, ok := d.(*common.CacheDetails)
				if assert.True(t, ok, "ElastiCache legacy details must be *common.CacheDetails, got %T", d) {
					assert.Equal(t, "redis", cd.Engine)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)
			mockFactory := new(MockProviderFactory)
			mockProvider := new(MockProvider)
			mockServiceClient := new(MockServiceClient)
			mockSTS := new(MockSTSClient)
			assertMocksOnCleanup(t, mockStore, mockEmail, mockFactory, mockProvider, mockServiceClient, mockSTS)

			plan := &config.PurchasePlan{ID: "plan-" + tc.name, Name: "Plan"}
			exec := &config.PurchaseExecution{
				ExecutionID: "exec-" + tc.name,
				PlanID:      "plan-" + tc.name,
				Recommendations: []config.RecommendationRecord{
					{
						CommitmentCurrency: "USD",
						Provider:           "aws",
						Service:            tc.service,
						ResourceType:       tc.resource,
						Engine:             tc.engine,
						// Details deliberately empty — simulates a
						// row persisted before #453.
						Region:      tc.region,
						Count:       1,
						Term:        1,
						Payment:     "All Upfront",
						Savings:     10.0,
						UpfrontCost: 100.0,
						Selected:    true,
					},
				},
			}

			var capturedRec common.Recommendation
			mockStore.On("GetPurchasePlan", ctx, plan.ID).Return(plan, nil)
			mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
			mockEmail.On("SendPurchaseConfirmation", ctx, mock.AnythingOfType("email.NotificationData")).Return(nil)
			mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
			mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), tc.serviceType, tc.region).Return(mockServiceClient, nil)
			expectNoExistingCommitments(mockServiceClient)
			mockServiceClient.On(
				"PurchaseCommitment",
				mock.MatchedBy(hasPerRecDeadline(30*time.Second)),
				mock.AnythingOfType("common.Recommendation"),
				mock.AnythingOfType("common.PurchaseOptions"),
			).Run(func(args mock.Arguments) {
				capturedRec = args.Get(1).(common.Recommendation)
			}).Return(
				common.PurchaseResult{Success: true, CommitmentID: "ri-" + tc.name},
				nil,
			)
			mockSTS.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts error"))

			manager := &Manager{
				config:          mockStore,
				email:           mockEmail,
				stsClient:       mockSTS,
				providerFactory: mockFactory,
				dashboardURL:    "https://dashboard.example.com",
			}

			err := manager.executePurchase(ctx, exec)
			require.NoError(t, err, "legacy empty-Details rec must still purchase cleanly")
			require.NotNil(t, capturedRec.Details, "rec.Details handed to the cloud client must be non-nil even for legacy rows")
			tc.assertDetails(t, capturedRec.Details)
		})
	}
}
