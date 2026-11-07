package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/managedredis"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func redisRec(provider common.ProviderType, service common.ServiceType, account string) common.Recommendation {
	return common.Recommendation{
		Provider: provider, Account: account, Service: service, Region: "westeurope",
		ResourceType: "C1", Count: 3, Term: "1yr", PaymentOption: "all-upfront",
	}
}

// advisorRedisRecs returns the Advisor-sourced Azure Redis rec in its real
// pre-bump shape (cache, no Details) and post-bump shape (memorydb with
// managedredis.Details, go#702 providers/azure/recommendations.go:654-656).
func advisorRedisRecs() (before, after common.Recommendation) {
	before = redisRec(common.ProviderAzure, common.ServiceCache, "sub-1")
	after = redisRec(common.ProviderAzure, common.ServiceMemoryDB, "sub-1")
	after.Details = managedredis.Details(after.ResourceType)
	return before, after
}

// go#702 emits Azure Redis as memorydb; the Consumption rec keeps its former
// cache ID, and an Advisor-only rec shifts ID once because it gains engine
// "redis" (accepted; suppressions are engine-canonicalised instead).
func TestConvertRecommendations_AzureMemoryDBStoredAsCache(t *testing.T) {
	s := &Scheduler{}
	consumption := redisRec(common.ProviderAzure, common.ServiceMemoryDB, "sub-1")
	consumption.Details = managedredis.Details(consumption.ResourceType)
	_, advisor := advisorRedisRecs()
	want := s.convertRecommendations([]common.Recommendation{consumption}, "azure")
	got := s.convertRecommendations([]common.Recommendation{advisor}, "azure")
	require.Len(t, want, 1)
	require.Len(t, got, 1)
	assert.Equal(t, "cache", got[0].Service)
	assert.Equal(t, "redis", got[0].Engine)
	assert.Equal(t, want[0].ID, got[0].ID)
}

func TestApplySuppressions_AdvisorRedisEngineStableAcrossGoBump(t *testing.T) {
	s := &Scheduler{}
	before, after := advisorRedisRecs()
	oldRec := s.convertRecommendations([]common.Recommendation{before}, "azure")[0]
	newRec := s.convertRecommendations([]common.Recommendation{after}, "azure")[0]
	require.Equal(t, "", oldRec.Engine)
	require.Equal(t, "redis", newRec.Engine)
	for _, tc := range []struct {
		name      string
		supEngine string
		rec       config.RecommendationRecord
	}{
		{"old suppression hides new rec", oldRec.Engine, newRec},
		{"new suppression hides old rec", newRec.Engine, oldRec},
		{"same-engine control", newRec.Engine, newRec},
	} {
		rec := tc.rec
		rec.CloudAccountID = strPtr("acct-1")
		store := &mockSuppressionStore{
			recs: []config.RecommendationRecord{rec},
			sups: []config.PurchaseSuppression{{
				ExecutionID: "e1", AccountID: "acct-1", Provider: "azure", Service: "cache",
				Region: rec.Region, ResourceType: rec.ResourceType, Engine: tc.supEngine,
				SuppressedCount: 1, ExpiresAt: time.Now().Add(24 * time.Hour),
			}},
		}
		got, err := (&Scheduler{config: store}).ListRecommendations(context.Background(), config.RecommendationFilter{})
		require.NoError(t, err, tc.name)
		require.Len(t, got, 1, tc.name)
		assert.Equal(t, rec.Count-1, got[0].Count, tc.name)
	}
}

func TestConvertRecommendations_AWSMemoryDBUnchanged(t *testing.T) {
	s := &Scheduler{}
	got := s.convertRecommendations([]common.Recommendation{redisRec(common.ProviderAWS, common.ServiceMemoryDB, "")}, "aws")
	require.Len(t, got, 1)
	assert.Equal(t, "memorydb", got[0].Service)
	assert.Equal(t, "aws||memorydb|westeurope|C1||1|all-upfront", got[0].ID)
}

func azureMemoryDBStored(t *testing.T, acct string) config.RecommendationRecord {
	t.Helper()
	s := &Scheduler{}
	out := s.convertRecommendations([]common.Recommendation{redisRec(common.ProviderAzure, common.ServiceMemoryDB, "")}, "azure")
	require.Len(t, out, 1)
	out[0].CloudAccountID = strPtr(acct)
	return out[0]
}

// The no-re-buy case: a suppression written under azure/cache still hides a
// memorydb-sourced Azure rec through the real ListRecommendations path.
func TestApplySuppressions_AzureCacheSuppressionHidesMemoryDBSourcedRec(t *testing.T) {
	rec := azureMemoryDBStored(t, "acct-1")
	store := &mockSuppressionStore{
		recs: []config.RecommendationRecord{rec},
		sups: []config.PurchaseSuppression{{
			ExecutionID: "e1", AccountID: "acct-1", Provider: "azure", Service: "cache",
			Region: rec.Region, ResourceType: rec.ResourceType, SuppressedCount: rec.Count,
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}},
	}
	s := &Scheduler{config: store}
	got, err := s.ListRecommendations(context.Background(), config.RecommendationFilter{})
	require.NoError(t, err)
	assert.Empty(t, got, "fully suppressed rec must not be offered again")
}

// azure/cache per-account toggles still apply to a memorydb-sourced rec.
func TestFilterRecsByResolvedConfigs_AzureCacheConfigAppliesToMemoryDBSourcedRec(t *testing.T) {
	rec := azureMemoryDBStored(t, "acct-1")
	key := config.AccountConfigKey("acct-1", "azure", "cache")
	for name, cfg := range map[string]*config.ServiceConfig{
		"disabled":  {Enabled: false},
		"min_count": {Enabled: true, MinCount: rec.Count + 1},
	} {
		got := filterRecsByResolvedConfigs([]config.RecommendationRecord{rec}, map[string]*config.ServiceConfig{key: cfg})
		assert.Empty(t, got, name)
	}
	got := filterRecsByResolvedConfigs([]config.RecommendationRecord{rec}, map[string]*config.ServiceConfig{key: {Enabled: true, MinCount: rec.Count}})
	assert.Len(t, got, 1)
}
