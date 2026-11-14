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

// Tests for mapServiceType to hit all branches.
//
// As of the Azure-purchase regression fix (issue #626), mapServiceType
// applies a canonical/legacy split: canonical slugs (compute, relational-db,
// cache, search, data-warehouse) map to the canonical ServiceType
// constants so Azure (and pre-emptively GCP) providers can resolve them
// in GetServiceClient; legacy AWS-only slugs (ec2, rds, elasticache,
// opensearch, redshift, memorydb) keep mapping to the legacy AWS
// constants for backward compat with rec rows persisted before the
// canonical normalisation. The AWS provider accepts both forms via its
// case-list union, so legacy spellings stay safe on AWS rec rows.
func TestMapServiceType_AllBranches(t *testing.T) {
	m := &Manager{}

	cases := []struct {
		input    string
		expected common.ServiceType
	}{
		// Canonical slugs must map to canonical ServiceType constants.
		// Issue #626: collapsing canonical onto AWS-legacy constants broke
		// every Azure (and pre-emptively GCP) approve for these service
		// families because their providers' GetServiceClient switches are
		// canonical-only.
		{"compute", common.ServiceCompute},
		{"relational-db", common.ServiceRelationalDB},
		{"nosql", common.ServiceNoSQL},
		{"cache", common.ServiceCache},
		{"search", common.ServiceSearch},
		{"data-warehouse", common.ServiceDataWarehouse},
		// Legacy AWS-only slugs preserved for backward compat (AWS provider
		// accepts both forms; older rec rows persisted before normalisation
		// still carry these spellings).
		{"ec2", common.ServiceEC2},
		{"rds", common.ServiceRDS},
		{"elasticache", common.ServiceElastiCache},
		{"opensearch", common.ServiceOpenSearch},
		{"redshift", common.ServiceRedshift},
		{"memorydb", common.ServiceMemoryDB},
		{"savingsplans", common.ServiceSavingsPlansAll},
		// Issue #85: the legacy hyphenated form is still accepted as a
		// backwards-compat alias so Lambda-scheduled purchase executions
		// persisted before the normalisation (rec.Service == "savings-plans"
		// in purchase_executions.recommendations JSONB) still map correctly
		// when re-executed. Once historical rows have aged out, the alias
		// arm can be dropped and this case should flip to ServiceType("savings-plans").
		{"savings-plans", common.ServiceSavingsPlansAll},
		{"unknown-service", common.ServiceType("unknown-service")},
		{"", common.ServiceType("")},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got := m.mapServiceType(tc.input)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// TestMapServiceType_AzureCanonicalTypesResolveOnAzureProvider is the
// regression test for the Azure purchase failure traced to execution
// 757c9a6e-b22a-4a81-bf04-b11043cbf658 ("unsupported service: ec2"):
// the previous mapping returned AWS-specific ServiceEC2 for both "ec2"
// and "compute", which broke every Azure VM purchase because the Azure
// provider's GetServiceClient switch only accepts ServiceCompute.
// Pinning the canonical-type contract here keeps a future refactor
// from re-introducing the bug.
func TestMapServiceType_AzureCanonicalTypesResolveOnAzureProvider(t *testing.T) {
	m := &Manager{}

	// "compute" is the slug Azure's collector writes for VM
	// recommendations (providers/azure/recommendations.go:
	// convertAdvisorRecommendation maps Microsoft.Compute ->
	// common.ServiceCompute). The mapping must round-trip to a value
	// the Azure provider can dispatch.
	azureInputs := []string{"compute", "relational-db", "cache", "search", "data-warehouse"}
	awsAliases := []common.ServiceType{
		common.ServiceEC2,
		common.ServiceRDS,
		common.ServiceElastiCache,
		common.ServiceOpenSearch,
		common.ServiceRedshift,
	}
	for _, slug := range azureInputs {
		got := m.mapServiceType(slug)
		for _, alias := range awsAliases {
			assert.NotEqual(t, alias, got, "slug %q must not map to AWS-specific %s", slug, alias)
		}
	}
}

// Tests for resolveAccountProvider with unknown provider.
func TestResolveAccountProvider_UnknownProvider(t *testing.T) {
	m := &Manager{}
	account := config.CloudAccount{
		ID:       "acc-1",
		Provider: "unknown-cloud",
	}
	result, err := m.resolveAccountProvider(context.Background(), account)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown cloud provider")
	assert.Nil(t, result)
}

// Tests for resolveAWSProvider when assumeRoleSTS is nil.
func TestResolveAWSProvider_NoSTS(t *testing.T) {
	m := &Manager{
		assumeRoleSTS: nil,
	}
	account := config.CloudAccount{
		ID:          "acc-aws",
		Provider:    "aws",
		AWSAuthMode: "assume_role",
		AWSRoleARN:  "arn:aws:iam::123456789012:role/testrole",
	}
	result, err := m.resolveAWSProvider(context.Background(), account)
	// Without STS, returns error (not silent nil)
	assert.Error(t, err)
	assert.Nil(t, result)
}

// Issue #402: purchasing for a role_arn account with no role ARN must not use
// the host's ambient credentials unless the account is the host.
func TestResolveAWSProvider_RoleARNWithoutARNOnNonHostFailsClosed(t *testing.T) {
	ctx := context.Background()
	hostSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, hostSTS)
	hostSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(&sts.GetCallerIdentityOutput{Account: aws.String("111111111111")}, nil)
	m := &Manager{
		assumeRoleSTS:   new(MockAssumeRoleSTS),
		stsClient:       hostSTS,
		ambientAWSCreds: aws.AnonymousCredentials{},
	}
	account := config.CloudAccount{ID: "tenant", Provider: "aws", ExternalID: "222222222222", AWSAuthMode: "role_arn"}

	result, err := m.resolveAWSProvider(ctx, account)
	require.ErrorIs(t, err, credentials.ErrNotHostAccount)
	assert.Nil(t, result)

	account.ExternalID = "111111111111"
	result, err = m.resolveAWSProvider(ctx, account)
	require.NoError(t, err)
	assert.Equal(t, aws.AnonymousCredentials{}, result.AWSCredentialsProvider)
}

// Tests for resolveAzureProvider without credStore and not managed_identity — returns error.
func TestResolveAzureProvider_NoCredStoreNoManagedIdentity(t *testing.T) {
	m := &Manager{
		credStore: nil,
	}
	account := config.CloudAccount{
		ID:            "acc-azure",
		Provider:      "azure",
		AzureAuthMode: "service_principal", // not managed_identity
	}
	result, err := m.resolveAzureProvider(context.Background(), account)
	assert.Error(t, err)
	assert.Nil(t, result)
}

// Tests for resolveGCPProvider without credStore and not application_default — returns error.
func TestResolveGCPProvider_NoCredStoreNoADC(t *testing.T) {
	m := &Manager{
		credStore: nil,
	}
	account := config.CloudAccount{
		ID:          "acc-gcp",
		Provider:    "gcp",
		GCPAuthMode: "service_account_key", // not application_default
	}
	result, err := m.resolveGCPProvider(context.Background(), account)
	assert.Error(t, err)
	assert.Nil(t, result)
}

// Tests for resolveGCPProvider with application_default (nil credStore is okay).
func TestResolveGCPProvider_ApplicationDefault(t *testing.T) {
	m := &Manager{
		credStore: nil,
	}
	account := config.CloudAccount{
		ID:           "acc-gcp-adc",
		Provider:     "gcp",
		GCPAuthMode:  "application_default",
		GCPProjectID: "my-project",
	}
	// application_default → returns (nil, nil) since ADC is ambient
	result, err := m.resolveGCPProvider(context.Background(), account)
	assert.NoError(t, err)
	assert.Nil(t, result)
}

// Tests for the claim funnel: a claimed notified row runs to a successful purchase.
func TestClaimAndExecuteWithin_NotifiedRowExecutes(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockSTS := new(MockSTSClient)

	plan := &config.PurchasePlan{
		ID:                "plan-approved",
		Name:              "Approved Plan",
		AutoPurchase:      true,
		MaxStepCommitment: capOf(1e9),
		RampSchedule: config.RampSchedule{
			TotalSteps:  4,
			CurrentStep: 0,
		},
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-approved",
		PlanID:      "plan-approved",
		Status:      "notified",
		StepNumber:  1,
		Recommendations: []config.RecommendationRecord{{
			CommitmentCurrency: "USD",
			Provider:           "aws", Service: "ec2", ResourceType: "m5.large", Region: "us-east-1",
			Count: 1, UpfrontCost: 100, Selected: true,
		}},
	}

	mockStore.On("GetGlobalConfig", mock.Anything).Return(autoPurchaseOnCfg(), nil).Maybe()
	// claimAndExecuteWithin CASes the row to "running" before executing (issue #1013).
	runningExec := *exec
	runningExec.Status = "running"
	mockStore.On("TransitionExecutionStatus", ctx, "exec-approved",
		[]string{"pending", "notified"}, "running", (*string)(nil)).Return(&runningExec, nil)
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-approved").Return(plan, nil)
	mockEmail.On("SendPurchaseConfirmation", mock.Anything, mock.AnythingOfType("email.NotificationData")).Return(nil)
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockStore.On("CompletePlanStep", mock.Anything, "plan-approved", 1).Return(nil)
	mockStore.On("SavePurchaseHistory", mock.Anything, mock.AnythingOfType("*config.PurchaseHistoryRecord")).Return(nil)
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(nil, errors.New("sts error"))
	mockProv := new(MockProvider)
	mockSvc := new(MockServiceClient)
	assertMocksOnCleanup(t, mockEmail, mockFactory, mockSTS, mockProv, mockSvc)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProv, nil)
	mockProv.On("GetServiceClient", mock.Anything, common.ServiceEC2, "us-east-1").Return(mockSvc, nil)
	mockSvc.On("PurchaseCommitment", mock.Anything, mock.Anything, mock.Anything).
		Return(common.PurchaseResult{Success: true, CommitmentID: "ri-approved"}, nil)

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		stsClient:       mockSTS,
		dashboardURL:    "https://dashboard.example.com",
	}

	claimed, err := manager.claimAndExecuteWithin(ctx, exec, 0)
	require.NoError(t, err)
	require.True(t, claimed)

	mockStore.AssertExpectations(t)
}

// Tests for the claim funnel: SavePurchaseExecution error after failed purchase.
func TestClaimAndExecuteWithin_SaveError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := allowFailedNotification(new(MockEmailSender))
	mockFactory := new(MockProviderFactory)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockFactory, mockSTS)

	plan := &config.PurchasePlan{
		ID:                "plan-save-err",
		Name:              "Plan",
		AutoPurchase:      true,
		MaxStepCommitment: capOf(1e9),
	}

	rec := config.RecommendationRecord{
		CommitmentCurrency: "USD",
		Provider:           "aws",
		Service:            "ec2",
		ResourceType:       "m5.large",
		Region:             "us-east-1",
		Count:              1,
		UpfrontCost:        100,
		Selected:           true,
	}

	exec := &config.PurchaseExecution{
		ExecutionID:     "exec-save-err",
		PlanID:          "plan-save-err",
		Status:          "notified",
		Recommendations: []config.RecommendationRecord{rec},
	}

	mockStore.On("GetGlobalConfig", mock.Anything).Return(autoPurchaseOnCfg(), nil)
	// claimAndExecuteWithin CASes the row to "running" before executing (issue #1013).
	runningExec := *exec
	runningExec.Status = "running"
	mockStore.On("TransitionExecutionStatus", ctx, "exec-save-err",
		[]string{"pending", "notified"}, "running", (*string)(nil)).Return(&runningExec, nil)
	mockStore.On("GetPurchasePlan", mock.Anything, "plan-save-err").Return(plan, nil)
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(errors.New("save failed"))
	mockSTS.On("GetCallerIdentity", mock.Anything, mock.Anything).Return(nil, errors.New("sts error"))

	// Provider factory returns error → purchase fails
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(nil, errors.New("provider error"))

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		providerFactory: mockFactory,
		stsClient:       mockSTS,
		dashboardURL:    "https://dashboard.example.com",
	}

	claimed, err := manager.claimAndExecuteWithin(ctx, exec, 0)
	require.True(t, claimed)
	// The terminal-save failure surfaces from executeAndFinalize as
	// ErrAuditLoss (the row is stranded in "running"). A single-account
	// provider failure with no committed recs is not multi-account-ackable,
	// so it is returned.
	assert.Error(t, err)
	assert.ErrorIs(t, err, config.ErrAuditLoss)
}

// TestManager_ApproveExecution_PropagatesPlanID pins that a non-empty PlanID
// reaches GetPurchasePlan through the approve->execute chain. Earlier versions
// used "" and would have silently passed even if the plan id were dropped or
// substituted en route.
func TestManager_ApproveExecution_PropagatesPlanID(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)

	accountID := "acct-1"
	// Non-empty PlanID exercises the plan-id propagation through the
	// approve→execute chain. Earlier versions of this test used "" and
	// would have silently passed even if the approve handler dropped or
	// substituted the plan id en route to GetPurchasePlan.
	planID := "plan-appv"
	exec := &config.PurchaseExecution{
		ExecutionID:   "exec-appv",
		PlanID:        planID,
		Status:        "pending",
		ApprovalToken: config.HashApprovalToken("correct-token"),
		// Recommendations are non-Selected, so processPurchaseRecommendations
		// is a no-op and no AWS API call is made.
		Recommendations: []config.RecommendationRecord{
			{CloudAccountID: &accountID},
		},
	}
	approved := &config.PurchaseExecution{
		ExecutionID:     "exec-appv",
		PlanID:          planID,
		Status:          "approved",
		ApprovalToken:   config.HashApprovalToken("correct-token"),
		StepNumber:      1,
		Recommendations: exec.Recommendations,
	}

	// rotateApprovalToken adds no further fetch: it mutates `approved` (the
	// row TransitionExecutionStatus returns) in place.
	mockStore.On("GetExecutionByID", ctx, "exec-appv").Return(exec, nil).Times(1)
	// Atomic approve transition (issue #372 fix).
	mockStore.On("TransitionExecutionStatusTokenPinned", ctx, "exec-appv", []string{"pending", "notified"}, "running", (*string)(nil), "correct-token").Return(approved, nil)
	// Synchronous execute chain: GetPurchasePlan is called with the
	// approved execution's PlanID — pinning the non-empty id here means a
	// regression that drops it (e.g. passes "" or exec.ExecutionID by
	// mistake) would mismatch the mock expectation and fail the test.
	plan := &config.PurchasePlan{ID: planID, Name: "test-plan"}
	mockStore.On("GetPurchasePlan", mock.Anything, planID).Return(plan, nil)
	// Nothing is selected, so nothing is bought and no "purchase confirmed"
	// email goes out (#339).
	mockStore.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil)
	mockStore.On("CompletePlanStep", mock.Anything, planID, 1).Return(nil)

	manager := &Manager{
		config:       mockStore,
		email:        mockEmail,
		dashboardURL: "https://dashboard.example.com",
	}

	_, _, err := manager.ApproveExecution(ctx, "exec-appv", "correct-token", "owner@example.com", nil)
	require.NoError(t, err)
	mockStore.AssertExpectations(t)
	mockEmail.AssertNotCalled(t, "SendPurchaseConfirmation", mock.Anything, mock.Anything)
	mockEmail.AssertExpectations(t)
}

// Tests for executeSinglePurchase error paths via executePurchase

func TestManager_ExecuteSinglePurchase_ProviderError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockFactory, mockSTS)

	plan := &config.PurchasePlan{
		ID:   "plan-prov-err",
		Name: "Test Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-prov-err",
		PlanID:      "plan-prov-err",
		StepNumber:  1,
		Recommendations: []config.RecommendationRecord{
			{
				Provider:     "aws",
				Service:      "ec2",
				ResourceType: "m5.large",
				Region:       "us-east-1",
				Count:        1,
				Savings:      100.0,
				Selected:     true,
			},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-prov-err").Return(plan, nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(nil, errors.New("provider unavailable"))
	mockSTS.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts error"))

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "some purchases failed")
	assert.Equal(t, "failed to create aws provider: provider unavailable", exec.Recommendations[0].Error)
}

func TestManager_ExecuteSinglePurchase_ServiceClientError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockFactory, mockProvider, mockSTS)

	plan := &config.PurchasePlan{
		ID:   "plan-svc-err",
		Name: "Test Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-svc-err",
		PlanID:      "plan-svc-err",
		StepNumber:  1,
		Recommendations: []config.RecommendationRecord{
			{
				Provider:     "aws",
				Service:      "rds",
				ResourceType: "db.r5.large",
				Region:       "eu-west-1",
				Count:        2,
				Savings:      200.0,
				Selected:     true,
			},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-svc-err").Return(plan, nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceRDS, "eu-west-1").Return(nil, errors.New("service client error"))
	mockSTS.On("GetCallerIdentity", ctx, mock.Anything).Return(nil, errors.New("sts error"))

	manager := &Manager{
		config:          mockStore,
		email:           mockEmail,
		stsClient:       mockSTS,
		providerFactory: mockFactory,
		dashboardURL:    "https://dashboard.example.com",
	}

	err := manager.executePurchase(ctx, exec)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "some purchases failed")
	assert.Contains(t, exec.Recommendations[0].Error, "failed to get service client")
}

func TestManager_ExecuteSinglePurchase_PurchaseNotSuccessful(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockFactory, mockProvider, mockServiceClient, mockSTS)

	plan := &config.PurchasePlan{
		ID:   "plan-not-success",
		Name: "Test Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-not-success",
		PlanID:      "plan-not-success",
		Recommendations: []config.RecommendationRecord{
			{
				Provider:     "aws",
				Service:      "elasticache",
				ResourceType: "cache.r5.large",
				Region:       "ap-southeast-1",
				Count:        1,
				Savings:      50.0,
				Selected:     true,
			},
		},
	}

	mockStore.On("GetPurchasePlan", ctx, "plan-not-success").Return(plan, nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceElastiCache, "ap-southeast-1").Return(mockServiceClient, nil)
	mockServiceClient.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(
		common.PurchaseResult{Success: false}, nil,
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
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "some purchases failed")
	assert.Contains(t, exec.Recommendations[0].Error, "purchase was not successful")
}

func TestManager_ExecuteSinglePurchase_PurchaseNotSuccessful_WithError(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockEmail := new(MockEmailSender)
	mockFactory := new(MockProviderFactory)
	mockProvider := new(MockProvider)
	mockServiceClient := new(MockServiceClient)
	mockSTS := new(MockSTSClient)
	assertMocksOnCleanup(t, mockStore, mockFactory, mockProvider, mockServiceClient, mockSTS)

	plan := &config.PurchasePlan{
		ID:   "plan-err-result",
		Name: "Test Plan",
	}

	exec := &config.PurchaseExecution{
		ExecutionID: "exec-err-result",
		PlanID:      "plan-err-result",
		Recommendations: []config.RecommendationRecord{
			{
				Provider:     "aws",
				Service:      "opensearch",
				ResourceType: "r5.large.search",
				Region:       "us-west-2",
				Count:        1,
				Savings:      75.0,
				Selected:     true,
			},
		},
	}

	specificErr := errors.New("capacity limit exceeded")
	mockStore.On("GetPurchasePlan", ctx, "plan-err-result").Return(plan, nil)
	mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProvider, nil)
	mockProvider.On("GetServiceClient", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), common.ServiceOpenSearch, "us-west-2").Return(mockServiceClient, nil)
	mockServiceClient.On("PurchaseCommitment", mock.MatchedBy(hasPerRecDeadline(30*time.Second)), mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions")).Return(
		common.PurchaseResult{Success: false, Error: specificErr}, nil,
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
	assert.Error(t, err)
	assert.Contains(t, exec.Recommendations[0].Error, "capacity limit exceeded")
}
