package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

type failingDirectListStore struct {
	*directListStore
	pairsErr, listErr error
}

func (s *failingDirectListStore) ListUnresolvedRIExchangeDirectAccountRegions(ctx context.Context) ([]config.RIExchangeDirectAccountRegion, error) {
	if s.pairsErr != nil {
		return nil, s.pairsErr
	}
	return s.directListStore.ListUnresolvedRIExchangeDirectAccountRegions(ctx)
}

func (s *failingDirectListStore) ListUnresolvedRIExchangeDirectExecutions(ctx context.Context, f config.RIExchangeDirectListFilter) (*config.RIExchangeDirectListPage, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.directListStore.ListUnresolvedRIExchangeDirectExecutions(ctx, f)
}

func callDirectList(t *testing.T, store *failingDirectListStore, authErr error) (any, error) {
	t.Helper()
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID}, nil).Maybe()
	mockAuth.On("HasPermissionAPI", mock.Anything, mock.Anything, "execute", "ri-exchange").Return(true, nil).Maybe()
	mockAuth.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).Return([]string(nil), nil).Maybe()
	if authErr != nil {
		mockAuth.On("HasPermissionForConstraintsAPI", mock.Anything, mock.Anything, "execute", "ri-exchange", mock.Anything).Return(false, authErr)
	} else {
		mockAuth.On("HasPermissionForConstraintsAPI", mock.Anything, mock.Anything, "execute", "ri-exchange", mock.Anything).Return(true, nil).Maybe()
	}
	h := &Handler{config: store, auth: mockAuth}
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}}
	return h.listRIExchangeDirectExecutions(context.Background(), req)
}

// A failure anywhere in the visibility chain is a server error with no
// partial list: nothing is shown on a guess.
func TestListRIExchangeDirectExecutions_FailsClosed(t *testing.T) {
	rows := []config.RIExchangeDirectRow{
		dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1),
		dlRow(dlID2, dlPtr(dlAcctB), "eu-west-1", "unknown", 2),
	}
	boom := errors.New("backend down")
	call := callDirectList
	tests := []struct {
		name  string
		store *failingDirectListStore
		auth  error
	}{
		{"the pair query fails", &failingDirectListStore{directListStore: dlStore(rows...), pairsErr: boom}, nil},
		{"the page query fails", &failingDirectListStore{directListStore: dlStore(rows...), listErr: boom}, nil},
		{"the constraint check fails with a non-403 error", &failingDirectListStore{directListStore: dlStore(rows...)}, boom},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := call(t, tc.store, tc.auth)
			require.Error(t, err)
			_, isClient := IsClientError(err)
			assert.False(t, isClient, "a backend failure is a 5xx, not a refusal: %v", err)
			assert.Nil(t, out, "no partial list")
		})
	}
	t.Run("control: the same setup lists both rows", func(t *testing.T) {
		out, err := call(t, &failingDirectListStore{directListStore: dlStore(rows...)}, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{dlID1, dlID2}, dlIDs(t, out))
	})
}

// Before migration 163 the list answers 503, whichever of its two store reads
// notices first; it is not a generic 500.
func TestListRIExchangeDirectExecutions_NotReadyIs503(t *testing.T) {
	rows := []config.RIExchangeDirectRow{dlRow(dlID1, dlPtr(dlAcctA), "eu-west-1", "unknown", 1)}
	notReady := fmt.Errorf("list unresolved direct exchange rows: %w", config.ErrRIExchangeDirectResolveNotReady)
	for name, store := range map[string]*failingDirectListStore{
		"the pair query": {directListStore: dlStore(rows...), pairsErr: notReady},
		"the page query": {directListStore: dlStore(rows...), listErr: notReady},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := callDirectList(t, store, nil)
			require.Error(t, err)
			assert.Equal(t, 503, clientCode(t, err))
			assert.Nil(t, out)
		})
	}
}
