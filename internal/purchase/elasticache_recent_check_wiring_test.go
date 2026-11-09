package purchase

// #1863 S2: the ElastiCache recent-reservation check wired into the real
// executor path (the real elasticache.Client over the shared fake API).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// expectNoExistingCommitments is the shared expectation for tests that drive an
// AWS ElastiCache rec through executeSinglePurchase with a MockServiceClient:
// the check lists live reservations first, and none exist. Maybe, because the
// same table also holds recs that never list.
func expectNoExistingCommitments(svc *MockServiceClient) {
	svc.On("GetExistingCommitments", mock.Anything).Return([]common.Commitment(nil), nil).Maybe()
}

// runECExecution runs one AWS ElastiCache rec through the real fan-out. The
// client is returned for the rec's own region, and the rec's region is the one
// recorded in ecRegion, so the listing and the recommendation agree.
func runECExecution(t *testing.T, api *fakeElastiCacheAPI, recs ...config.RecommendationRecord) (*config.PurchaseExecution, []string, bool) {
	t.Helper()
	return runECExecutionWithBudgets(t, api, purchaseBudgets{}, recs...)
}

func runECExecutionWithBudgets(t *testing.T, api *fakeElastiCacheAPI, budgets purchaseBudgets, recs ...config.RecommendationRecord) (*config.PurchaseExecution, []string, bool) {
	t.Helper()
	ctx := context.Background()
	store, sender, factory, prov := new(MockConfigStore), new(MockEmailSender), new(MockProviderFactory), new(MockProvider)
	assertMocksOnCleanup(t, store, sender, factory, prov)
	store.SavePurchaseExecutionFn = func(context.Context, *config.PurchaseExecution) error { return nil }
	store.On("SavePurchaseHistory", ctx, mock.Anything).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil)

	if len(recs) == 0 {
		recs = []config.RecommendationRecord{ecCacheRec(t, "elasticache")}
	}
	// Exact region matchers pin that the client is built for rec.Region. A rec in
	// another region gets its own client over an empty fake (nothing held there).
	for _, region := range recRegions(recs) {
		regionAPI := api
		if region != ecRegion {
			regionAPI = &fakeElastiCacheAPI{}
		}
		prov.On("GetServiceClient", mock.Anything, common.ServiceElastiCache, region).Return(provider.ServiceClient(newFakeElastiCacheClient(regionAPI)), nil)
	}
	for i := range recs {
		recs[i].ID = fmt.Sprintf("rec-1863-%d", i)
	}
	exec := &config.PurchaseExecution{ExecutionID: "exec-1863", IdempotencyKey: "lineage-1863-wiring", Recommendations: recs}
	m := &Manager{config: store, email: sender, providerFactory: factory, budgets: budgets}
	_, _, errs, allRefused, err := m.processPurchaseRecommendations(ctx, exec, &config.PurchasePlan{Name: "p"}, "acct", (*provider.Config)(nil), "")
	require.NoError(t, err)
	return exec, errs, allRefused
}

func recRegions(recs []config.RecommendationRecord) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range recs {
		if !seen[r.Region] {
			seen[r.Region] = true
			out = append(out, r.Region)
		}
	}
	return out
}

func TestElastiCacheWiring_RecentSamePoolReservationRefusesBeforePurchase(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("other-lineage", "redis", 2, time.Hour)}}

	exec, errs, allRefused := runECExecution(t, api)

	assert.Empty(t, api.purchases(), "the purchase call must never be made")
	assert.True(t, allRefused, "the row ends as a before-submission refusal")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], elastiCacheRecentReservationNote)
	assert.False(t, exec.Recommendations[0].Purchased)
}

// Regression guard (passes on main too): the library's by-ID guard adopts the
// execution's own listed reservation, so the check must not refuse it.
func TestElastiCacheWiring_OwnListedReservationReachesPurchaseAndIsAdopted(t *testing.T) {
	own := common.IdempotentReservationID(elastiCacheReservationIDPrefix, common.DeriveIdempotencyToken("lineage-1863-wiring", 0))
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld(own, "redis", 2, time.Minute)}}

	exec, errs, allRefused := runECExecution(t, api)

	assert.Empty(t, errs)
	assert.False(t, allRefused)
	assert.True(t, exec.Recommendations[0].Purchased, "the own reservation is adopted, not refused")
}

func ecOwnID(i int) string {
	return common.IdempotentReservationID(elastiCacheReservationIDPrefix, common.DeriveIdempotencyToken("lineage-1863-wiring", i))
}

// Sibling recs of one execution never refuse each other: the sibling's listed
// reservation is in the own-ID set.
func TestElastiCacheWiring_SiblingOwnReservationDoesNotRefuse(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld(ecOwnID(1), "redis", 2, time.Minute)}}
	second := ecCacheRec(t, "elasticache")
	second.ResourceType = "cache.r6g.xlarge"

	_, errs, allRefused := runECExecution(t, api, ecCacheRec(t, "elasticache"), second)

	assert.Empty(t, errs)
	assert.False(t, allRefused)
}

// A lost-response retry beside another lineage's same-pool reservation: only the
// self ID (sawSelf) rescues it from the refusal.
func TestElastiCacheWiring_RetryWithForeignReservationIsAdopted(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{
		ecHeld(ecOwnID(0), "redis", 2, time.Minute), ecHeld("other-lineage", "redis", 2, time.Hour)}}

	exec, errs, allRefused := runECExecution(t, api)

	assert.Empty(t, errs)
	assert.False(t, allRefused)
	assert.True(t, exec.Recommendations[0].Purchased)
}

func TestPreSubmitChecks_NilOwnNamesDoesNotPanic(t *testing.T) {
	svc := new(MockServiceClient)
	expectNoExistingCommitments(svc)
	ecRecord := ecCacheRec(t, "cache")
	gcpRecord := config.RecommendationRecord{Provider: "gcp", Service: "compute", Region: "us-central1", ResourceType: "n2-standard-4", Count: 1}
	gcpRec := common.Recommendation{Provider: common.ProviderGCP, Service: common.ServiceCompute, Region: "us-central1", ResourceType: "n2-standard-4", Count: 1}

	assert.NotPanics(t, func() {
		_, err := preSubmitChecks(context.Background(), svc, gcpRecord, gcpRec, common.PurchaseOptions{}, nil)
		assert.NoError(t, err)
		_, err = preSubmitChecks(context.Background(), svc, ecRecord, ecRec("redis", 2), common.PurchaseOptions{}, nil)
		assert.NoError(t, err)
	})
}

func TestPreSubmitChecks_ElastiCacheRefusesBeforeDispatchTimeIsStamped(t *testing.T) {
	held := newFakeElastiCacheClient(&fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("other", "redis", 2, time.Hour)}})
	clean := newFakeElastiCacheClient(&fakeElastiCacheAPI{})
	record := ecCacheRec(t, "cache")

	opts, err := preSubmitChecks(context.Background(), held, record, ecRec("redis", 2), common.PurchaseOptions{}, nil)
	requireRefusal(t, err, elastiCacheRecentReservationNote)
	assert.True(t, opts.RequestedAt.IsZero(), "a refusal returns before withDispatchTime")

	opts, err = preSubmitChecks(context.Background(), clean, record, ecRec("redis", 2), common.PurchaseOptions{}, nil)
	require.NoError(t, err)
	assert.False(t, opts.RequestedAt.IsZero(), "a pass continues to withDispatchTime")
}

func TestPreSubmitChecks_OnlyAWSElastiCacheListsReservations(t *testing.T) {
	cases := map[string]common.Recommendation{
		"aws ec2":      {Provider: common.ProviderAWS, Service: common.ServiceCompute},
		"aws rds":      {Provider: common.ProviderAWS, Service: common.ServiceRelationalDB},
		"aws memorydb": {Provider: common.ProviderAWS, Service: common.ServiceMemoryDB},
		"azure cache":  {Provider: common.ProviderAzure, Service: common.ServiceCache},
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			rec.Count = 1
			svc := new(MockServiceClient) // no GetExistingCommitments expectation: a call panics
			_, err := preSubmitChecks(context.Background(), svc, config.RecommendationRecord{}, rec, common.PurchaseOptions{}, nil)
			assert.NoError(t, err)
		})
	}
}
