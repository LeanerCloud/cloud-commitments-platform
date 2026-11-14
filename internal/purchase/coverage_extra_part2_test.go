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

// TestManager_ExecuteSinglePurchase_DetailsByService is the regression guard
// for issue #453. Before the fix, executeSinglePurchase assigned a value-
// typed common.DatabaseDetails (and only when rec.Engine was non-empty);
// every AWS service client's findOfferingID type-asserts a *pointer*, so the
// assertion failed and surfaced as "invalid service details for <Service>"
// for every dashboard-driven purchase after #373 made approve synchronous.
//
// The revised fix (post-design-review) persists the full ServiceDetails
// payload onto the RecommendationRecord at collection time and reconstructs
// the correct typed *Details pointer in executeSinglePurchase. This table-
// driven test:
//
//  1. Seeds a rec with the canonical Details JSON for each service (the
//     "happy path" — new rows post-fix carry full details).
//  2. Captures the rec.Details passed to the cloud client and asserts
//     both the concrete pointer type AND that every field round-tripped
//     intact (Platform=Windows must come through, not be silently
//     defaulted to Linux/UNIX).
//
// A second sub-test below covers the legacy fallback (empty Details JSON).
func TestManager_ExecuteSinglePurchase_DetailsByService(t *testing.T) {
	cases := []struct {
		details       common.ServiceDetails
		assertDetails func(t *testing.T, d common.ServiceDetails)
		name          string
		service       string
		serviceType   common.ServiceType
		region        string
		resource      string
		engine        string
	}{
		{
			name:        "ec2_windows",
			service:     "ec2",
			serviceType: common.ServiceEC2,
			region:      "us-east-1",
			resource:    "t4g.nano",
			details:     &common.ComputeDetails{InstanceType: "t4g.nano", Platform: "Windows", Tenancy: "dedicated", Scope: "Region"},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				cd, ok := d.(*common.ComputeDetails)
				if assert.True(t, ok, "EC2 details must be *common.ComputeDetails, got %T", d) {
					// Platform must round-trip — silent fallback to
					// Linux/UNIX is precisely the silent mis-purchase
					// the revised fix exists to prevent.
					assert.Equal(t, "Windows", cd.Platform)
					assert.Equal(t, "dedicated", cd.Tenancy)
					assert.Equal(t, "Region", cd.Scope)
				}
			},
		},
		{
			name:        "rds_postgres",
			service:     "rds",
			serviceType: common.ServiceRDS,
			region:      "us-east-1",
			resource:    "db.r5.large",
			engine:      "postgres",
			details:     &common.DatabaseDetails{Engine: "postgres", AZConfig: "multi-az", InstanceClass: "db.r5.large"},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				dd, ok := d.(*common.DatabaseDetails)
				if assert.True(t, ok, "RDS details must be *common.DatabaseDetails, got %T", d) {
					assert.Equal(t, "postgres", dd.Engine)
					assert.Equal(t, "multi-az", dd.AZConfig)
					assert.Equal(t, "db.r5.large", dd.InstanceClass)
				}
			},
		},
		{
			name:        "elasticache_redis",
			service:     "elasticache",
			serviceType: common.ServiceElastiCache,
			region:      "us-east-1",
			resource:    "cache.r5.large",
			engine:      "redis",
			details:     &common.CacheDetails{Engine: "redis", NodeType: "cache.r5.large"},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				cd, ok := d.(*common.CacheDetails)
				if assert.True(t, ok, "ElastiCache details must be *common.CacheDetails, got %T", d) {
					assert.Equal(t, "redis", cd.Engine)
					assert.Equal(t, "cache.r5.large", cd.NodeType)
				}
			},
		},
		{
			name:        "opensearch",
			service:     "opensearch",
			serviceType: common.ServiceOpenSearch,
			region:      "us-west-2",
			resource:    "r5.large.search",
			details:     &common.SearchDetails{InstanceType: "r5.large.search"},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				sd, ok := d.(*common.SearchDetails)
				if assert.True(t, ok, "OpenSearch details must be *common.SearchDetails, got %T", d) {
					assert.Equal(t, "r5.large.search", sd.InstanceType)
				}
			},
		},
		{
			name:        "redshift",
			service:     "redshift",
			serviceType: common.ServiceRedshift,
			region:      "us-east-1",
			resource:    "ra3.xlplus",
			details:     &common.DataWarehouseDetails{NodeType: "ra3.xlplus", NumberOfNodes: 2, ClusterType: "multi-node"},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				dw, ok := d.(*common.DataWarehouseDetails)
				if assert.True(t, ok, "Redshift details must be *common.DataWarehouseDetails, got %T", d) {
					assert.Equal(t, "ra3.xlplus", dw.NodeType)
					assert.Equal(t, 2, dw.NumberOfNodes)
				}
			},
		},
		{
			name:        "sp_compute",
			service:     "savings-plans-compute",
			serviceType: common.ServiceSavingsPlansCompute,
			region:      "us-east-1",
			resource:    "ComputeSP",
			details:     &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 1.50},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				sp, ok := d.(*common.SavingsPlanDetails)
				if assert.True(t, ok, "SP-Compute details must be *common.SavingsPlanDetails, got %T", d) {
					assert.Equal(t, "Compute", sp.PlanType)
					assert.InDelta(t, 1.50, sp.HourlyCommitment, 0.001)
				}
			},
		},
		{
			name:        "sp_ec2instance",
			service:     "savings-plans-ec2instance",
			serviceType: common.ServiceSavingsPlansEC2Instance,
			region:      "us-east-1",
			resource:    "EC2InstanceSP",
			details:     &common.SavingsPlanDetails{PlanType: "EC2Instance", HourlyCommitment: 2.0},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				sp, ok := d.(*common.SavingsPlanDetails)
				if assert.True(t, ok, "SP-EC2Instance details must be *common.SavingsPlanDetails, got %T", d) {
					assert.Equal(t, "EC2Instance", sp.PlanType)
				}
			},
		},
		{
			// Legacy umbrella slug ("savings-plans") that mapSavingsPlansSlug
			// still accepts for purchase_execution rows persisted before the
			// rename in PR #94. DecodeServiceDetailsFor must also recognize
			// this alias so a stored "savings-plans" rec round-trips through
			// the codec the same way as the canonical "savingsplans" form.
			// Regression guard: without the "savings-plans" case in
			// newDetailsForService, DecodeServiceDetailsFor returns (nil,
			// false) and recommendation.Details is nil — the SP client's
			// findOfferingID type-assertion then fails with "invalid service
			// details" on legacy rows, re-introducing #453 for SP umbrella.
			name:        "sp_legacy_umbrella",
			service:     "savings-plans",
			serviceType: common.ServiceSavingsPlansAll,
			region:      "us-east-1",
			resource:    "LegacySP",
			details:     &common.SavingsPlanDetails{PlanType: "Compute", HourlyCommitment: 0.75},
			assertDetails: func(t *testing.T, d common.ServiceDetails) {
				sp, ok := d.(*common.SavingsPlanDetails)
				if assert.True(t, ok, "legacy SP umbrella must decode to *common.SavingsPlanDetails, got %T", d) {
					assert.Equal(t, "Compute", sp.PlanType)
					assert.InDelta(t, 0.75, sp.HourlyCommitment, 0.001)
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

			// Marshal the canonical Details into the RecommendationRecord
			// — this is exactly what the scheduler does at collection
			// time via common.MarshalServiceDetails, so the round-trip
			// here is the same one a real purchase exercises.
			detailsBlob, err := common.MarshalServiceDetails(tc.details)
			require.NoError(t, err)
			require.NotNil(t, detailsBlob, "test seed must produce a non-empty Details blob")

			plan := &config.PurchasePlan{ID: "plan-" + tc.name, Name: "Plan"}
			stampedAt := time.Now().UTC()
			exec := &config.PurchaseExecution{
				ExecutionID: "exec-" + tc.name,
				PlanID:      "plan-" + tc.name,
				// Redshift dispatch requires the lineage stamp executeAndFinalize mints.
				FirstDispatchAt: &stampedAt,
				Recommendations: []config.RecommendationRecord{
					{
						CommitmentCurrency: "USD",
						Provider:           "aws",
						Service:            tc.service,
						ResourceType:       tc.resource,
						Engine:             tc.engine,
						Details:            detailsBlob,
						Region:             tc.region,
						Count:              1,
						Term:               1,
						Payment:            "All Upfront",
						Savings:            10.0,
						UpfrontCost:        100.0,
						Selected:           true,
					},
				},
			}

			// Capture the rec passed to PurchaseCommitment so we can
			// inspect Details after the call.
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

			err = manager.executePurchase(ctx, exec)
			require.NoError(t, err, "purchase should not return the regression error 'invalid service details for <Service>'")
			assert.True(t, exec.Recommendations[0].Purchased, "rec should be marked purchased")
			assert.Empty(t, exec.Recommendations[0].Error, "rec error should be empty")
			require.NotNil(t, capturedRec.Details, "rec.Details handed to the cloud client must be non-nil")
			tc.assertDetails(t, capturedRec.Details)
		})
	}
}
