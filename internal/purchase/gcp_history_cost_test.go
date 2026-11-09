package purchase

import (
	"context"
	"errors"
	"testing"

	"cloud.google.com/go/compute/apiv1/computepb"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/services/computeengine"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/googleapis/gax-go/v2"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/googleapi"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type fakeCommitmentOp struct{}

func (fakeCommitmentOp) Wait(context.Context, ...gax.CallOption) error { return nil }

type fakeCommitmentsService struct{}

func (fakeCommitmentsService) List(context.Context, *computepb.ListRegionCommitmentsRequest) computeengine.CommitmentsIterator {
	return nil
}

func (fakeCommitmentsService) Insert(context.Context, *computepb.InsertRegionCommitmentRequest) (computeengine.CommitmentsOperation, error) {
	return fakeCommitmentOp{}, nil
}

// Get is only reached when an insert returns 409; this fake never does, so a
// call means the test no longer drives the path it was written for.
func (fakeCommitmentsService) Get(context.Context, *computepb.GetRegionCommitmentRequest) (*computepb.Commitment, error) {
	return nil, errors.New("fakeCommitmentsService.Get is not expected in this test")
}

func (fakeCommitmentsService) Close() error { return nil }

// TestSavePurchaseHistory_GCPRecordsNoUpfront drives the real library GCP client
// through the history write. GCP CUDs are billed monthly with no upfront charge,
// and analytics and inventory coverage add upfront_cost/term on top of
// monthly_cost, so recording the term total double-counts it. The
// cloud-commitments-go pin before a32fd1a returned the term total (1000 here).
func TestSavePurchaseHistory_GCPRecordsNoUpfront(t *testing.T) {
	ctx := context.Background()
	client, err := computeengine.NewClient("project", "us-central1")
	require.NoError(t, err)
	client.SetCommitmentsService(fakeCommitmentsService{})

	rec := common.Recommendation{
		ResourceType:   "n1-standard-1",
		Term:           "1yr",
		CommitmentCost: 1000,
		Count:          5,
		Details:        common.ComputeDetails{MemoryGB: 20},
	}
	result, err := client.PurchaseCommitment(ctx, rec, common.PurchaseOptions{})
	require.NoError(t, err)

	store := new(MockConfigStore)
	assertMocksOnCleanup(t, store)
	var saved *config.PurchaseHistoryRecord
	store.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchaseHistoryRecord) }).
		Return(nil)
	m := &Manager{config: store}
	monthly := 1000.0 / 12

	err = m.savePurchaseHistory(ctx,
		&config.PurchaseExecution{ExecutionID: "exec", PlanID: "plan"}, &config.PurchasePlan{ID: "plan"},
		config.RecommendationRecord{
			Provider: "gcp", Service: "compute", ResourceType: "n1-standard-1", Region: "us-central1",
			Count: 5, Term: 1, MonthlyCost: &monthly,
		},
		result, "account", ec2types.OfferingClassTypeConvertible)
	require.NoError(t, err)

	require.NotNil(t, saved)
	require.NotNil(t, saved.UpfrontCost)
	require.Zero(t, *saved.UpfrontCost)
	require.NotNil(t, saved.MonthlyCost)
	require.InDelta(t, monthly, *saved.MonthlyCost, 1e-9)
}

// conflictCommitmentsService rejects every Insert with a 409 and serves the
// commitment back from Get, as GCP does for a re-drive that finds the CUD an
// earlier attempt created.
type conflictCommitmentsService struct {
	fakeCommitmentsService
	inserted *computepb.InsertRegionCommitmentRequest
}

func (f *conflictCommitmentsService) Insert(_ context.Context, req *computepb.InsertRegionCommitmentRequest) (computeengine.CommitmentsOperation, error) {
	f.inserted = req
	return nil, &googleapi.Error{Code: 409, Message: "already exists"}
}

func (f *conflictCommitmentsService) Get(context.Context, *computepb.GetRegionCommitmentRequest) (*computepb.Commitment, error) {
	want := f.inserted.GetCommitmentResource()
	return &computepb.Commitment{
		Name:      want.Name,
		Plan:      want.Plan,
		Type:      want.Type,
		Resources: want.Resources,
	}, nil
}

// TestSavePurchaseHistory_AdoptedGCPRecordsZeroUpfront drives the real GCP client
// through an idempotent re-drive that adopts the earlier CUD. The library reports
// no Cost for it; a CUD has no upfront charge, so the row must hold 0, not NULL
// (NULL nulls the upfront aggregates for the whole term).
func TestSavePurchaseHistory_AdoptedGCPRecordsZeroUpfront(t *testing.T) {
	ctx := context.Background()
	client, err := computeengine.NewClient("project", "us-central1")
	require.NoError(t, err)
	client.SetCommitmentsService(&conflictCommitmentsService{})

	rec := common.Recommendation{
		ResourceType: "n1-standard-1",
		Term:         "1yr",
		Count:        5,
		Details:      common.ComputeDetails{MemoryGB: 20},
	}
	result, err := client.PurchaseCommitment(ctx, rec, common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("lineage-1856", 0)})
	require.NoError(t, err)
	require.True(t, result.ExistingCommitment)
	require.Nil(t, result.Cost, "the library contract this platform relies on: an adopted CUD reports no Cost")

	store := new(MockConfigStore)
	assertMocksOnCleanup(t, store)
	var saved *config.PurchaseHistoryRecord
	store.On("SavePurchaseHistory", ctx, mock.AnythingOfType("*config.PurchaseHistoryRecord")).
		Run(func(args mock.Arguments) { saved = args.Get(1).(*config.PurchaseHistoryRecord) }).
		Return(nil)
	m := &Manager{config: store}

	err = m.savePurchaseHistory(ctx,
		&config.PurchaseExecution{ExecutionID: "exec", PlanID: "plan"}, &config.PurchasePlan{ID: "plan"},
		config.RecommendationRecord{Provider: "gcp", Service: "compute", ResourceType: "n1-standard-1", Region: "us-central1", Count: 5, Term: 1},
		result, "account", ec2types.OfferingClassTypeConvertible)
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.NotNil(t, saved.UpfrontCost)
	require.Zero(t, *saved.UpfrontCost)
}

// TestHistoryUpfrontCostKeepsUnknownOutsideAdoptedGCPCompute pins the scope of
// the zero: AWS and Azure adoptions can hide a real upfront payment, and a
// fresh result with no Cost is a provider bug to surface, not a free purchase.
func TestHistoryUpfrontCostKeepsUnknownOutsideAdoptedGCPCompute(t *testing.T) {
	cases := []struct {
		name     string
		provider string
		service  common.ServiceType
		adopted  bool
	}{
		{"aws adopted", "aws", common.ServiceEC2, true},
		{"azure adopted", "azure", common.ServiceCompute, true},
		{"gcp non-compute adopted", "gcp", common.ServiceRDS, true},
		{"gcp compute fresh with no cost", "gcp", common.ServiceCompute, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Nil(t, historyUpfrontCost(tc.provider, tc.service, common.PurchaseResult{ExistingCommitment: tc.adopted}))
		})
	}

	known := 12.5
	got := historyUpfrontCost("gcp", common.ServiceCompute, common.PurchaseResult{Cost: &known, ExistingCommitment: true})
	require.Same(t, &known, got, "a reported cost is never overwritten")
}
