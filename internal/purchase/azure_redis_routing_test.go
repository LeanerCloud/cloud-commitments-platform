package purchase

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1801: only the managedredis client (ServiceMemoryDB) buys Azure Redis
// reservations; the Azure cache client refuses every purchase
// (cloud-commitments-go#694). A recommendation labeled "cache" must reach the
// right client for its provider, and keep its own label everywhere else.
func purchaseCacheRecThrough(t *testing.T, prov *MockProvider, providerName, region string, want common.ServiceType) {
	t.Helper()
	ctx := context.Background()
	store, sender, factory, svc := new(MockConfigStore), new(MockEmailSender), new(MockProviderFactory), new(MockServiceClient)
	assertMocksOnCleanup(t, store, sender, factory, prov, svc)
	store.SavePurchaseExecutionFn = func(context.Context, *config.PurchaseExecution) error { return nil }
	store.On("SavePurchaseHistory", ctx, mock.Anything).Return(nil)
	factory.On("CreateAndValidateProvider", mock.Anything, providerName, mock.Anything).Return(prov, nil)
	prov.On("GetServiceClient", mock.Anything, want, region).Return(provider.ServiceClient(svc), nil)
	expectNoExistingCommitments(svc)
	svc.On("PurchaseCommitment", mock.Anything, mock.MatchedBy(func(rec common.Recommendation) bool {
		return rec.Service == common.ServiceCache
	}), mock.Anything).Return(common.PurchaseResult{Success: true, CommitmentID: "order-1"}, nil)

	exec := &config.PurchaseExecution{ExecutionID: "exec-1801-" + providerName, IdempotencyKey: "lineage-1801-" + providerName,
		Recommendations: []config.RecommendationRecord{{ID: "rec-1", Provider: providerName, Service: "cache",
			ResourceType: "cache.r6g.large", Region: region, Count: 1, Term: 1, Selected: true}}}
	m := &Manager{config: store, email: sender, providerFactory: factory}
	_, _, errs, _, err := m.processPurchaseRecommendations(ctx, exec, &config.PurchasePlan{Name: "p"}, "acct", (*provider.Config)(nil), "")
	require.NoError(t, err)
	require.Empty(t, errs)
	require.True(t, exec.Recommendations[0].Purchased, "the purchase must reach the %s client", want)
}

// [fail-first] On main the Azure lookup asks for ServiceCache, which this
// mock provider does not serve, so the purchase fails.
func TestAzureCachePurchaseUsesTheManagedRedisClient(t *testing.T) {
	prov := new(MockProvider)
	purchaseCacheRecThrough(t, prov, "azure", "westeurope", common.ServiceMemoryDB)
	prov.AssertNotCalled(t, "GetServiceClient", mock.Anything, common.ServiceCache, mock.Anything)
}

// [guard] AWS "cache" stays on the cache (ElastiCache) client.
func TestAWSCachePurchaseKeepsTheCacheClient(t *testing.T) {
	prov := new(MockProvider)
	purchaseCacheRecThrough(t, prov, "aws", "us-east-1", common.ServiceCache)
	prov.AssertNotCalled(t, "GetServiceClient", mock.Anything, common.ServiceMemoryDB, mock.Anything)
}

func TestPurchaseClientService(t *testing.T) {
	for _, tc := range []struct {
		provider string
		in, want common.ServiceType
	}{
		{"azure", common.ServiceCache, common.ServiceMemoryDB},
		{"azure", common.ServiceMemoryDB, common.ServiceMemoryDB},
		{"azure", common.ServiceCompute, common.ServiceCompute},
		{"aws", common.ServiceCache, common.ServiceCache},
		{"gcp", common.ServiceCache, common.ServiceCache},
		{"", common.ServiceCache, common.ServiceCache},
	} {
		require.Equal(t, tc.want, purchaseClientService(tc.provider, tc.in), "%s/%s", tc.provider, tc.in)
	}
}
