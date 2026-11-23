package mocks

import (
	"context"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

func (m *MockConfigStore) UpsertRecommendations(ctx context.Context, collectedAt time.Time, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect) error {
	m.record("UpsertRecommendations", ctx, collectedAt, recs, successfulCollects)
	if !isExpected(&m.Mock, "UpsertRecommendations") {
		return nil
	}
	args := m.Called(ctx, collectedAt, recs, successfulCollects)
	return args.Error(0)
}

func (m *MockConfigStore) UpsertRecommendationsWithPartial(ctx context.Context, collectedAt time.Time, recs []config.RecommendationRecord, successfulCollects []config.SuccessfulCollect, partial *config.RecommendationsCollectionPartial) error {
	m.record("UpsertRecommendationsWithPartial", ctx, collectedAt, recs, successfulCollects, partial)
	if !isExpected(&m.Mock, "UpsertRecommendationsWithPartial") {
		return nil
	}
	args := m.Called(ctx, collectedAt, recs, successfulCollects, partial)
	return args.Error(0)
}

func (m *MockConfigStore) PruneStaleRecommendations(ctx context.Context, retentionDays int) (int64, error) {
	m.record("PruneStaleRecommendations", ctx, retentionDays)
	if !isExpected(&m.Mock, "PruneStaleRecommendations") {
		return 0, nil
	}
	args := m.Called(ctx, retentionDays)
	v, ok := args.Get(0).(int64)
	if !ok {
		m.reporter.wrongReturnType("PruneStaleRecommendations", "int64", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListStoredRecommendations(ctx context.Context, filter config.RecommendationFilter) ([]config.RecommendationRecord, error) {
	m.record("ListStoredRecommendations", ctx, filter)
	if !isExpected(&m.Mock, "ListStoredRecommendations") {
		return nil, nil
	}
	args := m.Called(ctx, filter)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RecommendationRecord)
	if !ok {
		m.reporter.wrongReturnType("ListStoredRecommendations", "[]config.RecommendationRecord", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListStoredRecommendationsChunk(ctx context.Context, filter config.RecommendationFilter, after *config.RecommendationScanKey, limit int) (config.RecommendationChunk, error) {
	m.record("ListStoredRecommendationsChunk", ctx, filter, after, limit)
	if !isExpected(&m.Mock, "ListStoredRecommendationsChunk") {
		return config.RecommendationChunk{Done: true}, nil
	}
	args := m.Called(ctx, filter, after, limit)
	v, ok := args.Get(0).(config.RecommendationChunk)
	if !ok {
		m.reporter.wrongReturnType("ListStoredRecommendationsChunk", "config.RecommendationChunk", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) ListStoredRecommendationTriples(ctx context.Context, scope config.RecommendationListScope) ([]config.RecommendationTriple, error) {
	m.record("ListStoredRecommendationTriples", ctx, scope)
	if !isExpected(&m.Mock, "ListStoredRecommendationTriples") {
		return nil, nil
	}
	args := m.Called(ctx, scope)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).([]config.RecommendationTriple)
	if !ok {
		m.reporter.wrongReturnType("ListStoredRecommendationTriples", "[]config.RecommendationTriple", args.Get(0))
	}
	return v, args.Error(1)
}

// PageStoredRecommendations defaults to an empty cache: an empty page with a
// zero summary.
func (m *MockConfigStore) PageStoredRecommendations(ctx context.Context, q config.RecommendationPageQuery) (config.RecommendationPage, error) {
	m.record("PageStoredRecommendations", ctx, q)
	if !isExpected(&m.Mock, "PageStoredRecommendations") {
		return config.RecommendationPage{}, nil
	}
	args := m.Called(ctx, q)
	v, ok := args.Get(0).(config.RecommendationPage)
	if !ok {
		m.reporter.wrongReturnType("PageStoredRecommendations", "config.RecommendationPage", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) GetRecommendationsFreshness(ctx context.Context) (*config.RecommendationsFreshness, error) {
	m.record("GetRecommendationsFreshness", ctx)
	if !isExpected(&m.Mock, "GetRecommendationsFreshness") {
		return &config.RecommendationsFreshness{}, nil
	}
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RecommendationsFreshness)
	if !ok {
		m.reporter.wrongReturnType("GetRecommendationsFreshness", "*config.RecommendationsFreshness", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) SetRecommendationsCollectionError(ctx context.Context, errMsg string) error {
	m.record("SetRecommendationsCollectionError", ctx, errMsg)
	if !isExpected(&m.Mock, "SetRecommendationsCollectionError") {
		return nil
	}
	args := m.Called(ctx, errMsg)
	return args.Error(0)
}

func (m *MockConfigStore) GetRIUtilizationCache(ctx context.Context, key config.RIUtilizationCacheKey) (*config.RIUtilizationCacheEntry, error) {
	m.record("GetRIUtilizationCache", ctx, key)
	if !isExpected(&m.Mock, "GetRIUtilizationCache") {
		return nil, nil
	}
	args := m.Called(ctx, key)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	v, ok := args.Get(0).(*config.RIUtilizationCacheEntry)
	if !ok {
		m.reporter.wrongReturnType("GetRIUtilizationCache", "*config.RIUtilizationCacheEntry", args.Get(0))
	}
	return v, args.Error(1)
}

func (m *MockConfigStore) UpsertRIUtilizationCache(ctx context.Context, key config.RIUtilizationCacheKey, payload []byte, fetchedAt time.Time) error {
	m.record("UpsertRIUtilizationCache", ctx, key, payload, fetchedAt)
	if !isExpected(&m.Mock, "UpsertRIUtilizationCache") {
		return nil
	}
	args := m.Called(ctx, key, payload, fetchedAt)
	return args.Error(0)
}
