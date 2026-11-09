package purchase

// Shared fake ElastiCache API (#1863 D10): the real library elasticache.Client
// runs against it through SetElastiCacheAPI, so the offering lookup, the by-ID
// idempotency guard, the listing and the purchase input are the pinned library's.

import (
	"context"
	"sync"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/elasticache"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsec "github.com/aws/aws-sdk-go-v2/service/elasticache"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// fakeElastiCacheAPI implements elasticache.API. held is what DescribeReservedCacheNodes
// returns; describeErr fails every such call. Purchases are recorded, not added to held.
type fakeElastiCacheAPI struct {
	mu          sync.Mutex
	held        []types.ReservedCacheNode
	describeErr error
	// block, when set, makes every no-ID listing wait for it or the caller's context.
	block     chan struct{}
	purchased []*awsec.PurchaseReservedCacheNodesOfferingInput
}

// DescribeReservedCacheNodesOfferings echoes the request filters, so the
// offering always passes the library's node type, engine, duration and payment checks.
func (f *fakeElastiCacheAPI) DescribeReservedCacheNodesOfferings(_ context.Context, in *awsec.DescribeReservedCacheNodesOfferingsInput, _ ...func(*awsec.Options)) (*awsec.DescribeReservedCacheNodesOfferingsOutput, error) {
	seconds := map[string]int32{"31536000": 31536000, "94608000": 94608000}[aws.ToString(in.Duration)]
	return &awsec.DescribeReservedCacheNodesOfferingsOutput{ReservedCacheNodesOfferings: []types.ReservedCacheNodesOffering{{
		ReservedCacheNodesOfferingId: aws.String("offering-1"),
		CacheNodeType:                in.CacheNodeType,
		ProductDescription:           in.ProductDescription,
		OfferingType:                 in.OfferingType,
		Duration:                     aws.Int32(seconds),
	}}}, nil
}

// DescribeReservedCacheNodes branches on the input: a by-ID call (the library's
// idempotency guard) returns the held reservation with that ID or
// ReservedCacheNodeNotFoundFault; a listing returns every held reservation.
func (f *fakeElastiCacheAPI) DescribeReservedCacheNodes(ctx context.Context, in *awsec.DescribeReservedCacheNodesInput, _ ...func(*awsec.Options)) (*awsec.DescribeReservedCacheNodesOutput, error) {
	if f.block != nil && in.ReservedCacheNodeId == nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	if in.ReservedCacheNodeId == nil {
		return &awsec.DescribeReservedCacheNodesOutput{ReservedCacheNodes: append([]types.ReservedCacheNode(nil), f.held...)}, nil
	}
	for _, n := range f.held {
		if aws.ToString(n.ReservedCacheNodeId) == *in.ReservedCacheNodeId {
			return &awsec.DescribeReservedCacheNodesOutput{ReservedCacheNodes: []types.ReservedCacheNode{n}}, nil
		}
	}
	return nil, &types.ReservedCacheNodeNotFoundFault{}
}

func (f *fakeElastiCacheAPI) PurchaseReservedCacheNodesOffering(_ context.Context, in *awsec.PurchaseReservedCacheNodesOfferingInput, _ ...func(*awsec.Options)) (*awsec.PurchaseReservedCacheNodesOfferingOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.purchased = append(f.purchased, in)
	return &awsec.PurchaseReservedCacheNodesOfferingOutput{ReservedCacheNode: &types.ReservedCacheNode{ReservedCacheNodeId: in.ReservedCacheNodeId}}, nil
}

func (f *fakeElastiCacheAPI) purchases() []*awsec.PurchaseReservedCacheNodesOfferingInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*awsec.PurchaseReservedCacheNodesOfferingInput(nil), f.purchased...)
}

const ecRegion = "us-east-1"

func newFakeElastiCacheClient(api *fakeElastiCacheAPI) *elasticache.Client {
	client := elasticache.NewClient(aws.Config{Region: ecRegion})
	client.SetElastiCacheAPI(api)
	return client
}

func ecCacheRec(t *testing.T, service string) config.RecommendationRecord {
	t.Helper()
	blob, err := common.MarshalServiceDetails(&common.CacheDetails{Engine: "redis"})
	require.NoError(t, err)
	return config.RecommendationRecord{
		Provider: "aws", Service: service, Region: ecRegion, ResourceType: "cache.r6g.large",
		Count: 2, Term: 1, Payment: "no-upfront", Selected: true, Details: blob,
	}
}

func TestElastiCacheOwnReservationIDs_OnlySelectedAWSElastiCacheRecs(t *testing.T) {
	unselected := ecCacheRec(t, "cache")
	unselected.Selected = false
	azure := ecCacheRec(t, "cache")
	azure.Provider = "azure"
	exec := &config.PurchaseExecution{IdempotencyKey: "lineage-1863", Recommendations: []config.RecommendationRecord{
		ecCacheRec(t, "cache"), unselected, ecCacheRec(t, "elasticache"), azure, ecCacheRec(t, "memorydb")}}

	want := map[string]bool{}
	for _, i := range []int{0, 2} {
		want[common.IdempotentReservationID("elasticache-id-", common.DeriveIdempotencyToken("lineage-1863", i))] = true
	}
	assert.Equal(t, want, elastiCacheOwnReservationIDs(exec, selectedIndices(exec.Recommendations)))
}

// The mirror must equal the ReservedCacheNodeId the pinned library sends to AWS.
func TestElastiCacheReservationID_MatchesLibraryPurchaseID(t *testing.T) {
	api := &fakeElastiCacheAPI{}
	client := newFakeElastiCacheClient(api)
	exec := &config.PurchaseExecution{IdempotencyKey: "lineage-1863", Recommendations: []config.RecommendationRecord{ecCacheRec(t, "cache"), ecCacheRec(t, "cache")}}
	rec := common.Recommendation{ResourceType: "cache.r6g.large", Region: ecRegion, Count: 2, Term: "1yr", PaymentOption: "no-upfront",
		Details: &common.CacheDetails{Engine: "redis"}}

	_, err := client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{IdempotencyToken: common.DeriveIdempotencyToken("lineage-1863", 1)})
	require.NoError(t, err)
	purchased := api.purchases()
	require.Len(t, purchased, 1)
	assert.True(t, elastiCacheOwnReservationIDs(exec, []int{1})[aws.ToString(purchased[0].ReservedCacheNodeId)])
}
