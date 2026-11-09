package purchase

// #1863 S3: executor-path scenarios for the ElastiCache recent-reservation
// check. The real elasticache.Client runs over the shared fake API, so every
// case goes through the real fan-out, offering lookup and by-ID guard. Cases
// marked "regression guard" pass on main too; the refusals fail there.

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

func ecRecWith(t *testing.T, engine string, count int, nodeType, region string) config.RecommendationRecord {
	t.Helper()
	rec := ecCacheRec(t, "elasticache")
	blob, err := common.MarshalServiceDetails(&common.CacheDetails{Engine: engine})
	require.NoError(t, err)
	rec.Details, rec.Count, rec.ResourceType, rec.Region = blob, count, nodeType, region
	return rec
}

func ecHeldWith(id string, age time.Duration, mutate func(*types.ReservedCacheNode)) types.ReservedCacheNode {
	n := ecHeld(id, "redis", 2, age)
	mutate(&n)
	return n
}

func TestElastiCacheScenarios_RecentSamePoolIsRefused(t *testing.T) {
	for _, state := range []string{"active", "payment-pending"} {
		t.Run(state, func(t *testing.T) {
			api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{
				ecHeldWith("other", time.Minute, func(n *types.ReservedCacheNode) { n.State = aws.String(state) })}}

			exec, errs, allRefused := runECExecution(t, api)

			assert.Empty(t, api.purchases())
			assert.True(t, allRefused, "no_submission: a before-submission refusal")
			require.Len(t, errs, 1)
			assert.Contains(t, errs[0], elastiCacheRecentReservationNote)
			assert.False(t, exec.Recommendations[0].Purchased)
		})
	}
}

func TestElastiCacheScenarios_ListingErrorFailsClosedWithNoteA(t *testing.T) {
	api := &fakeElastiCacheAPI{describeErr: assert.AnError}

	_, errs, allRefused := runECExecution(t, api)

	assert.Empty(t, api.purchases())
	assert.True(t, allRefused)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], elastiCacheDuplicateCheckFailedNote)
}

// Regression guard: the listed reservation is this rec's own ID, so the library
// guard adopts it. Distinct from the S2 smoke test: here the own listing is the
// only held reservation and ExistingCommitment must be set on the result.
func TestElastiCacheScenarios_OwnListedReservationIsAdopted(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld(ecOwnID(0), "redis", 2, time.Minute)}}

	exec, errs, _ := runECExecution(t, api)

	assert.Empty(t, errs)
	assert.Empty(t, api.purchases(), "adoption means the purchase call is skipped")
	assert.True(t, exec.Recommendations[0].Purchased)
}

// Regression guards for a different pool: each row is bought, not refused.
func TestElastiCacheScenarios_DifferentPoolIsPurchased(t *testing.T) {
	cases := map[string]struct {
		held types.ReservedCacheNode
		rec  func(*testing.T) config.RecommendationRecord
	}{
		"memcached held, redis rec": {ecHeld("o", "memcached", 2, time.Hour), nil},
		"valkey held, redis rec":    {ecHeld("o", "valkey", 2, time.Hour), nil},
		"other node type":           {ecHeldWith("o", time.Hour, func(n *types.ReservedCacheNode) { n.CacheNodeType = aws.String("cache.m6g.large") }), nil},
		"older than 24h":            {ecHeld("o", "redis", 2, 25*time.Hour), nil},
		"retired state":             {ecHeldWith("o", time.Hour, func(n *types.ReservedCacheNode) { n.State = aws.String("retired") }), nil},
		// The rec's region gets its own client, which lists nothing; the held one lives in us-east-1.
		"other region": {ecHeld("o", "redis", 2, time.Hour), func(t *testing.T) config.RecommendationRecord {
			return ecRecWith(t, "redis", 2, "cache.r6g.large", "eu-west-1")
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{c.held}}
			rec := ecCacheRec(t, "elasticache")
			if c.rec != nil {
				rec = c.rec(t)
			}

			exec, errs, allRefused := runECExecution(t, api, rec)

			assert.Empty(t, errs)
			assert.False(t, allRefused)
			assert.True(t, exec.Recommendations[0].Purchased)
			if rec.Region == ecRegion {
				assert.Len(t, api.purchases(), 1)
			} else {
				assert.Empty(t, api.purchases(), "the purchase went to the rec's own region client")
			}
		})
	}
}

func TestElastiCacheScenarios_Coverage(t *testing.T) {
	cases := []struct {
		name    string
		held    types.ReservedCacheNode
		engine  string
		count   int
		refused bool
	}{
		{"redis held 4 covers valkey rec 5", ecHeld("o", "redis", 4, time.Hour), "valkey", 5, true},
		// Regression guard: a valkey reservation does not cover a redis rec.
		{"valkey held, redis rec", ecHeld("o", "valkey", 4, time.Hour), "redis", 5, false},
		{"empty-engine wildcard held", ecHeld("o", "", 2, time.Hour), "redis", 2, true},
		{"partial cover held 2 rec 5", ecHeld("o", "redis", 2, time.Hour), "redis", 5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{c.held}}

			_, errs, allRefused := runECExecution(t, api, ecRecWith(t, c.engine, c.count, "cache.r6g.large", ecRegion))

			assert.Equal(t, c.refused, allRefused)
			if c.refused {
				assert.Empty(t, api.purchases())
				require.Len(t, errs, 1)
				assert.Contains(t, errs[0], elastiCacheRecentReservationNote)
			} else {
				assert.Empty(t, errs)
				assert.Len(t, api.purchases(), 1)
			}
		})
	}
}

// Exact node type key (D8): ElastiCache is size-flexible, yet a held xlarge does
// not refuse a large rec. Regression guard (passes on main).
func TestElastiCacheScenarios_HeldXlargeDoesNotRefuseLarge(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{
		ecHeldWith("o", time.Hour, func(n *types.ReservedCacheNode) { n.CacheNodeType = aws.String("cache.r6g.xlarge") })}}

	_, errs, allRefused := runECExecution(t, api)

	assert.Empty(t, errs)
	assert.False(t, allRefused)
	assert.Len(t, api.purchases(), 1)
}

// Regression guard: sibling recs of different pools are both bought.
func TestElastiCacheScenarios_SiblingDifferentPoolsBothBought(t *testing.T) {
	api := &fakeElastiCacheAPI{}

	_, errs, _ := runECExecution(t, api, ecRecWith(t, "redis", 2, "cache.r6g.large", ecRegion), ecRecWith(t, "redis", 2, "cache.r6g.xlarge", ecRegion))

	assert.Empty(t, errs)
	assert.Len(t, api.purchases(), 2)
}

// runECFinalize drives recs through executeAndFinalize, the only path that runs
// preExecutionRefusal, over the real elasticache client on the shared fake API.
func runECFinalize(t *testing.T, api *fakeElastiCacheAPI, recs ...config.RecommendationRecord) (*config.PurchaseExecution, error) {
	t.Helper()
	store, sender, factory, prov := new(MockConfigStore), new(MockEmailSender), new(MockProviderFactory), new(MockProvider)
	store.SavePurchaseExecutionFn = func(context.Context, *config.PurchaseExecution) error { return nil }
	store.On("SavePurchaseHistory", mock.Anything, mock.Anything).Return(nil).Maybe()
	sender.On("SendPurchaseConfirmation", mock.Anything, mock.Anything).Return(nil).Maybe()
	sender.On("SendPurchaseFailedNotification", mock.Anything, mock.Anything).Return(nil).Maybe()
	factory.On("CreateAndValidateProvider", mock.Anything, "aws", mock.Anything).Return(prov, nil).Maybe()
	prov.On("GetServiceClient", mock.Anything, common.ServiceElastiCache, ecRegion).Return(provider.ServiceClient(newFakeElastiCacheClient(api)), nil).Maybe()
	exec := &config.PurchaseExecution{ExecutionID: "exec-1867", IdempotencyKey: "lineage-1867", Status: "running", Recommendations: recs}
	m := &Manager{config: store, email: sender, providerFactory: factory, credStore: awsAccessKeyCredStore()}
	return exec, m.executeAndFinalize(context.Background(), exec)
}

// #1867: two recs of the SAME pool in one execution would each get their own
// token and race their listings, so the selection is refused before submission.
func TestElastiCacheScenarios_SamePoolTwiceInOneExecutionIsRefused(t *testing.T) {
	api := &fakeElastiCacheAPI{}

	exec, err := runECFinalize(t, api, ecCacheRec(t, "elasticache"), ecCacheRec(t, "elasticache"))

	require.Error(t, err)
	assert.Equal(t, elastiCacheDuplicatePoolRefusal, err.Error())
	assert.Empty(t, api.purchases())
	assert.Equal(t, "failed", exec.Status)
}

// Control: with the twin unselected there is no duplicate, so the one rec buys.
func TestElastiCacheScenarios_SamePoolTwinUnselectedBuysOnce(t *testing.T) {
	api := &fakeElastiCacheAPI{}
	twin := ecCacheRec(t, "elasticache")
	twin.Selected = false

	_, err := runECFinalize(t, api, ecCacheRec(t, "elasticache"), twin)

	require.NoError(t, err)
	assert.Len(t, api.purchases(), 1)
}

// Regression guard: a same-pool sibling's own reservation is listed (its purchase
// landed before a retry). Only the own-ID filter keeps it from refusing rec 0.
func TestElastiCacheScenarios_SamePoolSiblingOwnReservationIsFilteredOut(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld(ecOwnID(1), "redis", 2, time.Minute)}}

	_, errs, allRefused := runECExecution(t, api, ecCacheRec(t, "elasticache"), ecCacheRec(t, "elasticache"))

	assert.Empty(t, errs)
	assert.False(t, allRefused)
	assert.Len(t, api.purchases(), 1, "rec 0 buys; rec 1 adopts its own listed reservation")
}

// Decision order on the executor path (regression guard, fails only if the
// sawSelf-first order breaks): self AND another lineage's reservation are listed.
func TestElastiCacheScenarios_DecisionOrderSelfBeatsForeignReservation(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{
		ecHeld(ecOwnID(0), "redis", 5, time.Minute), ecHeld("other-lineage", "redis", 5, time.Hour)}}

	exec, errs, allRefused := runECExecution(t, api, ecRecWith(t, "redis", 5, "cache.r6g.large", ecRegion))

	assert.Empty(t, errs)
	assert.False(t, allRefused)
	assert.True(t, exec.Recommendations[0].Purchased)
}

// The library's own validation still refuses a degenerate rec; the check neither
// masks nor replaces it. No reservation is held, so the check cannot refuse first.
func TestElastiCacheScenarios_LibraryRefusalIsNotMasked(t *testing.T) {
	cases := map[string]config.RecommendationRecord{
		"empty engine": ecRecWith(t, "", 2, "cache.r6g.large", ecRegion),
		"zero count":   ecRecWith(t, "redis", 0, "cache.r6g.large", ecRegion),
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			api := &fakeElastiCacheAPI{}

			exec, errs, _ := runECExecution(t, api, rec)

			assert.Empty(t, api.purchases())
			assert.False(t, exec.Recommendations[0].Purchased)
			require.Len(t, errs, 1)
			assert.NotContains(t, errs[0], elastiCacheRecentReservationNote)
			assert.NotContains(t, errs[0], elastiCacheDuplicateCheckFailedNote)
		})
	}
}

// A listing slower than the per-rec budget is refused, never purchased. The
// budget is injected short, so the test does not wait for the real 30s.
func TestElastiCacheScenarios_SlowListingIsRefusedNotPurchased(t *testing.T) {
	api := &fakeElastiCacheAPI{block: make(chan struct{})}
	t.Cleanup(func() { close(api.block) })

	start := time.Now()
	_, errs, allRefused := runECExecutionWithBudgets(t, api, purchaseBudgets{perRec: 200 * time.Millisecond}, ecCacheRec(t, "elasticache"))

	assert.Less(t, time.Since(start), 10*time.Second)
	assert.Empty(t, api.purchases())
	assert.True(t, allRefused)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0], elastiCacheDuplicateCheckFailedNote)
}
