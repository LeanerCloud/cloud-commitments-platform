package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type timelineTestStore struct {
	*MockConfigStore
	called bool
}

func (s *timelineTestStore) ListLadderTimeline(_ context.Context, accountID, provider string, cursor *config.LadderTimelineCursor) (*config.LadderTimelinePage, error) {
	s.called = true
	return &config.LadderTimelinePage{Events: []config.LadderTimelineEvent{{ID: accountID, Layer: "compute-sp"}}, TotalCount: 1, TotalUSDHr: "1.000000"}, nil
}

func (s *timelineTestStore) ListLadderTimelineRuns(_ context.Context, accountID, provider string, cursor *config.LadderTimelineCursor) (*config.LadderTimelineRunPage, error) {
	s.called = true
	return &config.LadderTimelineRunPage{Runs: []config.LadderTimelineRun{{
		ID: accountID, DelayedFirstStep: true,
		TrancheCounts: config.LadderTrancheCounts{Scheduled: 2, Fired: 1, Completed: 3, Failed: 4, Canceled: 5},
	}}, TotalCount: 1}, nil
}

func TestLadderTimelineRoutedScope(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/ladder/tranches", "/api/ladder/runs"} {
		for _, accountID := range []string{scopedInAccount, scopedOutAccount} {
			t.Run(path+accountID, func(t *testing.T) {
				h, original := ladderScopedHandler(t)
				store := &timelineTestStore{MockConfigStore: original}
				h.config = store
				req := ladderScopedReq("")
				req.QueryStringParameters = map[string]string{"account_id": accountID, "provider": "aws"}
				req.QueryStringParameters["after_created_at"] = "2026-10-08T00:00:00Z"
				req.QueryStringParameters["after_id"] = scopedInAccount
				result, err := NewRouter(h).Route(context.Background(), "GET", path, req)
				if accountID == scopedOutAccount {
					require.ErrorIs(t, err, errNotFound)
					require.False(t, store.called)
					require.Nil(t, result)
					return
				}
				require.NoError(t, err)
				require.True(t, store.called)
				require.NotNil(t, result)
			})
		}
	}
}

func TestLadderTimelinePermissionConstraints(t *testing.T) {
	t.Parallel()
	for _, permissions := range [][]auth.Permission{
		{},
		{{Action: auth.ActionView, Resource: auth.ResourceConfig, Constraints: &auth.PermissionConstraints{Providers: []string{"azure"}}}},
	} {
		mockAuth := new(MockAuthService)
		mockAuth.On("ValidateSession", mock.Anything, scopedToken).Return(&Session{UserID: scopedUserID}, nil).Maybe()
		mockAuth.grantPermissionsScoped(permissions, nil)
		original := new(MockConfigStore)
		original.On("GetCloudAccount", mock.Anything, scopedInAccount).
			Return(&config.CloudAccount{ID: scopedInAccount, Provider: "aws"}, nil).Maybe()
		store := &timelineTestStore{MockConfigStore: original}
		h := &Handler{config: store, auth: mockAuth}
		req := ladderScopedReq("")
		req.QueryStringParameters = map[string]string{"account_id": scopedInAccount, "provider": "aws"}
		_, err := NewRouter(h).Route(context.Background(), "GET", "/api/ladder/tranches", req)
		require.Error(t, err)
		require.False(t, store.called)
	}
}

func TestLadderTimelineUnavailableIsNotEmpty(t *testing.T) {
	t.Parallel()
	h, _ := ladderScopedHandler(t)
	req := ladderScopedReq("")
	req.QueryStringParameters = map[string]string{"account_id": scopedInAccount, "provider": "aws"}
	result, err := NewRouter(h).Route(context.Background(), "GET", "/api/ladder/tranches", req)
	clientErr, ok := IsClientError(err)
	require.True(t, ok)
	require.Equal(t, 503, clientErr.code)
	require.Nil(t, result)
}

func TestLadderTimelineUnsupportedProvider(t *testing.T) {
	t.Parallel()
	h, original := scopedHandler(t, scopedInAccount)
	t.Cleanup(func() { original.AssertExpectations(t) })
	original.On("GetCloudAccount", mock.Anything, scopedInAccount).
		Return(&config.CloudAccount{ID: scopedInAccount, Name: "azure", Provider: "azure"}, nil)
	store := &timelineTestStore{MockConfigStore: original}
	h.config = store
	req := ladderScopedReq("")
	req.QueryStringParameters = map[string]string{"account_id": scopedInAccount, "provider": "azure"}
	_, err := NewRouter(h).Route(context.Background(), "GET", "/api/ladder/tranches", req)
	clientErr, ok := IsClientError(err)
	require.True(t, ok)
	require.Equal(t, 501, clientErr.code)
	require.False(t, store.called)
}

func TestLadderTimelineCursorRejectsPartialAndInvalidInput(t *testing.T) {
	t.Parallel()
	for _, params := range []map[string]string{
		{"after_id": scopedInAccount},
		{"after_created_at": "2026-10-08T00:00:00Z"},
		{"after_created_at": "not-a-date", "after_id": scopedInAccount},
	} {
		cursor, err := ladderTimelineCursor(params)
		require.Error(t, err)
		require.Nil(t, cursor)
	}
}

type failingTimelineStore struct{ timelineTestStore }

func (s *failingTimelineStore) ListLadderTimeline(context.Context, string, string, *config.LadderTimelineCursor) (*config.LadderTimelinePage, error) {
	return nil, fmt.Errorf("row 9: %w", config.ErrLadderTimelineRate)
}

func TestLadderTimelineMalformedRateReturnsFixedError(t *testing.T) {
	t.Parallel()
	h, original := ladderScopedHandler(t)
	h.config = &failingTimelineStore{timelineTestStore{MockConfigStore: original}}
	req := ladderScopedReq("")
	req.QueryStringParameters = map[string]string{"account_id": scopedInAccount, "provider": "aws"}
	result, err := NewRouter(h).Route(context.Background(), "GET", "/api/ladder/tranches", req)
	clientErr, ok := IsClientError(err)
	require.True(t, ok)
	require.Equal(t, 500, clientErr.code)
	require.NotContains(t, clientErr.Error(), "row 9")
	require.Nil(t, result)
}

// TestLadderTimelineNonUUIDAccountIDIs400 pins #1430 for both timeline reads:
// an account_id that is not a UUID answers 400 instead of letting Postgres's
// uuid syntax error surface as a 500.
func TestLadderTimelineNonUUIDAccountIDIs400(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/api/ladder/tranches", "/api/ladder/runs"} {
		t.Run(path, func(t *testing.T) {
			h, original := ladderScopedHandler(t)
			failNonUUIDLookups(original)
			store := &timelineTestStore{MockConfigStore: original}
			h.config = store
			req := ladderScopedReq("")
			req.QueryStringParameters = map[string]string{"account_id": ladderBadAccountID, "provider": "aws"}
			result, err := NewRouter(h).Route(context.Background(), "GET", path, req)
			requireInvalidAccountID(t, original, result, err)
			require.False(t, store.called)
		})
	}
}

func TestLadderTimelineRunsExposeTrancheCounts(t *testing.T) {
	t.Parallel()
	h, original := ladderScopedHandler(t)
	h.config = &timelineTestStore{MockConfigStore: original}
	req := ladderScopedReq("")
	req.QueryStringParameters = map[string]string{"account_id": scopedInAccount, "provider": "aws"}
	result, err := NewRouter(h).Route(context.Background(), "GET", "/api/ladder/runs", req)
	require.NoError(t, err)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"tranche_counts":{"scheduled":2,"fired":1,"completed":3,"failed":4,"canceled":5}`)
	require.Contains(t, string(raw), `"delayed_first_step":true`)
}
