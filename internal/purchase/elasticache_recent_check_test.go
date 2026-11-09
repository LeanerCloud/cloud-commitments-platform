package purchase

// #1863 S1: the ElastiCache recent-reservation check. Most cases run the real
// library elasticache.Client over the shared fake API (elasticache_fake_api_test.go),
// so the listing, the engine mapping and recfilter's generic path are the pinned ones.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/elasticache/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ecHeld(id, engine string, count int32, age time.Duration) types.ReservedCacheNode {
	return types.ReservedCacheNode{
		ReservedCacheNodeId: aws.String(id), CacheNodeType: aws.String("cache.r6g.large"),
		ProductDescription: aws.String(engine), CacheNodeCount: aws.Int32(count),
		State: aws.String("active"), StartTime: aws.Time(time.Now().Add(-age)),
	}
}

func ecRec(engine string, count int) common.Recommendation {
	return common.Recommendation{
		Provider: common.ProviderAWS, Service: common.ServiceCache, Region: ecRegion,
		ResourceType: "cache.r6g.large", Count: count, Details: &common.CacheDetails{Engine: engine},
	}
}

func ecCheck(t *testing.T, api *fakeElastiCacheAPI, rec common.Recommendation, own map[string]bool, self string) error {
	t.Helper()
	return elastiCacheRecentRefusal(context.Background(), newFakeElastiCacheClient(api), rec, own, self)
}

func requireRefusal(t *testing.T, err error, note string) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, note, err.Error())
	var notSubmitted *beforeSubmissionError
	assert.True(t, errors.As(err, &notSubmitted), "a refusal is a beforeSubmission error")
}

func TestElastiCacheRecentRefusal_RecentReservationIsRefused(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("other", "redis", 2, time.Hour)}}
	requireRefusal(t, ecCheck(t, api, ecRec("redis", 2), nil, ""), elastiCacheRecentReservationNote)
}

func TestElastiCacheRecentRefusal_PartialCoverIsRefused(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("other", "redis", 2, time.Hour)}}
	requireRefusal(t, ecCheck(t, api, ecRec("redis", 5), nil, ""), elastiCacheRecentReservationNote)
}

func TestElastiCacheRecentRefusal_ListingErrorFailsClosed(t *testing.T) {
	api := &fakeElastiCacheAPI{describeErr: errors.New("throttled: secret provider text")}
	requireRefusal(t, ecCheck(t, api, ecRec("redis", 2), nil, ""), elastiCacheDuplicateCheckFailedNote)
}

// Regression guard (passes on main): a listing error cannot set sawSelf, so an
// own reservation that exists still fails closed.
func TestElastiCacheRecentRefusal_ListingErrorWithOwnReservationHeldStillRefuses(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("own", "redis", 2, time.Minute)}, describeErr: errors.New("boom")}
	requireRefusal(t, ecCheck(t, api, ecRec("redis", 2), map[string]bool{"own": true}, "own"), elastiCacheDuplicateCheckFailedNote)
}

func TestElastiCacheRecentRefusal_NotRefused(t *testing.T) {
	cases := map[string]struct {
		held []types.ReservedCacheNode
		rec  common.Recommendation
	}{
		"older than 24h":         {[]types.ReservedCacheNode{ecHeld("old", "redis", 2, 25*time.Hour)}, ecRec("redis", 2)},
		"memcached held":         {[]types.ReservedCacheNode{ecHeld("mc", "memcached", 2, time.Hour)}, ecRec("redis", 2)},
		"redis rec, valkey held": {[]types.ReservedCacheNode{ecHeld("vk", "valkey", 2, time.Hour)}, ecRec("redis", 2)},
		"nothing held":           {nil, ecRec("redis", 2)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, ecCheck(t, &fakeElastiCacheAPI{held: tc.held}, tc.rec, nil, ""))
		})
	}
}

func TestElastiCacheRecentRefusal_OtherRecsNeverTouchTheClient(t *testing.T) {
	for name, rec := range map[string]common.Recommendation{
		"azure cache": {Provider: common.ProviderAzure, Service: common.ServiceCache, Count: 1},
		"memorydb":    {Provider: common.ProviderAWS, Service: common.ServiceMemoryDB, Count: 1},
	} {
		t.Run(name, func(t *testing.T) {
			// A nil client would panic if the gate let the rec through.
			assert.NoError(t, elastiCacheRecentRefusal(context.Background(), nil, rec, nil, ""))
		})
	}
}

// A rec with Count <= 0 gets nil so the library's own validation refuses it;
// recfilter would mark it filtered and note B would be wrong for it.
func TestElastiCacheRecentRefusal_NonPositiveCountReturnsNil(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("other", "redis", 2, time.Hour)}}
	for _, count := range []int{0, -1} {
		assert.NoError(t, ecCheck(t, api, ecRec("redis", count), nil, ""))
	}
}

func TestElastiCacheRecentRefusal_UntrimmedEngineIsStillMatchedAndNotMutated(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("other", "redis", 2, time.Hour)}}
	rec := ecRec(" redis ", 2)

	requireRefusal(t, ecCheck(t, api, rec, nil, ""), elastiCacheRecentReservationNote)
	assert.Equal(t, " redis ", rec.Details.(*common.CacheDetails).Engine, "the rec that is bought keeps its engine")
}

func TestElastiCacheRecentRefusal_UnexpectedDetailsDoNotPanic(t *testing.T) {
	var typedNil *common.CacheDetails
	for name, details := range map[string]common.ServiceDetails{
		"nil":           nil,
		"typed nil":     typedNil,
		"not cache":     &common.ComputeDetails{},
		"value not ptr": common.CacheDetails{Engine: "redis"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := ecRec("redis", 2)
			rec.Details = details
			assert.NotPanics(t, func() { _ = ecCheck(t, &fakeElastiCacheAPI{}, rec, nil, "") })
		})
	}
}

func TestElastiCacheRecentRefusal_OwnReservationsAreDroppedFromTheListing(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("sibling", "redis", 2, time.Minute)}}
	assert.NoError(t, ecCheck(t, api, ecRec("redis", 2), map[string]bool{"sibling": true}, "mine"))
}

// Regression guard (passes on main): the own reservation listed wins over
// another lineage's overlapping reservation, whole or partial.
func TestElastiCacheRecentRefusal_OwnReservationListedWinsOverForeignOverlap(t *testing.T) {
	for name, foreign := range map[string]int32{"whole": 5, "partial": 1} {
		t.Run(name, func(t *testing.T) {
			api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{
				ecHeld("own", "redis", 2, time.Minute), ecHeld("foreign", "redis", foreign, time.Hour)}}
			assert.NoError(t, ecCheck(t, api, ecRec("redis", 5), map[string]bool{"own": true}, "own"))
		})
	}
}

// The scan covers the raw listing, so an own reservation outside the 24h window
// still sets sawSelf.
func TestElastiCacheRecentRefusal_OwnReservationOutsideLookbackStillWins(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{
		ecHeld("own", "redis", 2, 48*time.Hour), ecHeld("foreign", "redis", 2, time.Hour)}}
	assert.NoError(t, ecCheck(t, api, ecRec("redis", 2), map[string]bool{"own": true}, "own"))
}

func TestElastiCacheRecentRefusal_EmptySelfIsNotOwn(t *testing.T) {
	api := &fakeElastiCacheAPI{held: []types.ReservedCacheNode{ecHeld("", "redis", 2, time.Hour)}}
	requireRefusal(t, ecCheck(t, api, ecRec("redis", 2), nil, ""), elastiCacheRecentReservationNote)
}

// recfilter takes its engine-aware generic path only when the client has no
// FilterRecommendationsForRecentCommitments method.
func TestElastiCacheRecentDedupeClient_HasNoRecentCommitmentFilter(t *testing.T) {
	_, ok := any(elastiCacheRecentDedupeClient{}).(recentCommitmentFilter)
	assert.False(t, ok)
	_, ok = any(&elastiCacheRecentDedupeClient{}).(recentCommitmentFilter)
	assert.False(t, ok)
}

func TestElastiCacheRefusalFor_DecisionOrder(t *testing.T) {
	rec := ecRec("redis", 5)
	full, partial := []common.Recommendation{ecRec("redis", 5)}, []common.Recommendation{ecRec("redis", 3)}
	boom := errors.New("boom")
	cases := map[string]struct {
		passed, filtered []common.Recommendation
		err              error
		sawSelf          bool
		want             string
	}{
		"clean":                         {passed: full},
		"self wins over filtered":       {filtered: full, sawSelf: true},
		"self wins over partial":        {passed: partial, sawSelf: true},
		"self wins over error":          {err: boom, sawSelf: true},
		"filtered":                      {filtered: full, want: elastiCacheRecentReservationNote},
		"partial count":                 {passed: partial, want: elastiCacheRecentReservationNote},
		"error":                         {passed: full, err: boom, want: elastiCacheDuplicateCheckFailedNote},
		"error wins over filtered":      {filtered: full, err: boom, want: elastiCacheDuplicateCheckFailedNote},
		"nothing passed or filtered":    {want: elastiCacheDuplicateCheckFailedNote},
		"two passed (defensive branch)": {passed: append(append([]common.Recommendation{}, full...), full...), want: elastiCacheDuplicateCheckFailedNote},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := elastiCacheRefusalFor(tc.passed, tc.filtered, tc.err, tc.sawSelf, rec)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			requireRefusal(t, err, tc.want)
		})
	}
}
