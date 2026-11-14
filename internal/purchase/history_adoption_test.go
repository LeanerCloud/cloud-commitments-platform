package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// TestAdoptedOfferingClassRecordedInHistory drives a purchase result through
// processPurchaseRecommendations to the saved history row: an adopted RI must
// record the class AWS reports, not the class configured at this re-drive.
func TestAdoptedOfferingClassRecordedInHistory(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		service    string
		svcType    common.ServiceType
		want       string
		result     common.PurchaseResult
	}{
		{name: "adopted standard under convertible config", configured: "convertible", service: "ec2", svcType: common.ServiceEC2,
			result: common.PurchaseResult{ExistingCommitment: true, OfferingClass: "standard"}, want: "standard"},
		{name: "adopted convertible under standard config", configured: "standard", service: "compute", svcType: common.ServiceCompute,
			result: common.PurchaseResult{ExistingCommitment: true, OfferingClass: "convertible"}, want: "convertible"},
		{name: "adopted without a class is unknown", configured: "convertible", service: "ec2", svcType: common.ServiceEC2,
			result: common.PurchaseResult{ExistingCommitment: true}, want: ""},
		{name: "adopted with an unrecognized class is unknown", configured: "convertible", service: "ec2", svcType: common.ServiceEC2,
			result: common.PurchaseResult{ExistingCommitment: true, OfferingClass: "foo"}, want: ""},
		{name: "fresh buy without a class keeps the configured one", configured: "standard", service: "ec2", svcType: common.ServiceEC2,
			result: common.PurchaseResult{}, want: "standard"},
		{name: "reported class is lower-cased", configured: "convertible", service: "compute", svcType: common.ServiceCompute,
			result: common.PurchaseResult{ExistingCommitment: true, OfferingClass: "Standard"}, want: "standard"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := new(MockConfigStore)
			mockEmail := new(MockEmailSender)
			mockFactory := new(MockProviderFactory)
			mockProviderInst := new(MockProvider)
			mockServiceClient := new(MockServiceClient)
			assertMocksOnCleanup(t, mockStore, mockFactory, mockProviderInst, mockServiceClient)

			mockStore.On("GetGlobalConfig", ctx).Return(&config.GlobalConfig{OfferingClass: tc.configured}, nil)
			var history *config.PurchaseHistoryRecord
			mockStore.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).
				Run(func(args mock.Arguments) { history = args.Get(1).(*config.PurchaseHistoryRecord) }).
				Return(nil).Once()
			mockFactory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(mockProviderInst, nil)
			mockProviderInst.On("GetServiceClient", mock.Anything, tc.svcType, "us-east-1").Return(mockServiceClient, nil)
			res := tc.result
			res.Success, res.CommitmentID = true, "ri-1856"
			mockServiceClient.On("PurchaseCommitment", mock.Anything,
				mock.AnythingOfType("common.Recommendation"), mock.AnythingOfType("common.PurchaseOptions"),
			).Return(res, nil)

			manager := &Manager{config: mockStore, email: mockEmail, providerFactory: mockFactory}
			exec := &config.PurchaseExecution{
				ExecutionID: "exec-1856",
				Recommendations: []config.RecommendationRecord{
					{Provider: "aws", Service: tc.service, ResourceType: "m5.large", Region: "us-east-1", Count: 1, UpfrontCost: 300, Selected: true},
				},
			}

			_, _, errs, _, procErr := manager.processPurchaseRecommendations(ctx, exec, &config.PurchasePlan{Name: "Direct purchase"}, "111111111111", nil, "")
			require.NoError(t, procErr)
			require.Empty(t, errs)
			require.NotNil(t, history)
			require.Equal(t, tc.want, history.OfferingClass)
		})
	}
}

// TestAdoptedOfferingClassIgnoredOutsideAWSEC2 pins the provider and service
// gate: a reported class never lands on a non-EC2 or non-AWS row.
func TestAdoptedOfferingClassIgnoredOutsideAWSEC2(t *testing.T) {
	for _, tc := range []struct{ provider, service string }{
		{"azure", "compute"}, {"gcp", "compute"}, {"aws", "rds"},
	} {
		t.Run(tc.provider+"/"+tc.service, func(t *testing.T) {
			ctx := context.Background()
			store := new(MockConfigStore)
			assertMocksOnCleanup(t, store)
			var saved *config.PurchaseHistoryRecord
			store.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).
				Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchaseHistoryRecord) }).
				Return(nil)
			m := &Manager{config: store}

			err := m.savePurchaseHistory(ctx,
				&config.PurchaseExecution{ExecutionID: "exec"}, &config.PurchasePlan{ID: "plan"},
				config.RecommendationRecord{Provider: tc.provider, Service: tc.service, ResourceType: "x", Region: "r", Count: 1, Term: 1},
				common.PurchaseResult{Success: true, CommitmentID: "c-1", ExistingCommitment: true, OfferingClass: "standard"},
				"account", ec2types.OfferingClassTypeConvertible)
			require.NoError(t, err)
			require.NotNil(t, saved)
			require.Empty(t, saved.OfferingClass)
		})
	}
}
