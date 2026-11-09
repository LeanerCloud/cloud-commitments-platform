package api

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Handler tests for POST /api/ri-exchange/direct-executions/{id}/resolve
// (#1836). The store is a FIXTURE; the row lock, the settle age and the
// checked_at bounds are covered against real PostgreSQL in internal/config.

const (
	drRegion = "eu-west-1"
	// drOtherID is a second stuck row. Nothing returned for the first may name it.
	drOtherID = dlID4
)

var drResolvedAt = time.Date(2026, 11, 8, 9, 0, 0, 0, time.UTC)

type directResolveStore struct {
	*directListStore
	rows   map[string]config.RIExchangeDirectRow
	called int
	got    config.RIExchangeDirectResolve
	res    *config.RIExchangeDirectResolveResult
	err    error
}

func (s *directResolveStore) GetRIExchangeDirectExecution(_ context.Context, id string) (*config.RIExchangeDirectRow, error) {
	r, ok := s.rows[id]
	if !ok {
		return nil, config.ErrRIExchangeResolveNotFound
	}
	return &r, nil
}

func (s *directResolveStore) ResolveRIExchangeDirectExecution(_ context.Context, r config.RIExchangeDirectResolve) (*config.RIExchangeDirectResolveResult, error) {
	s.called++
	s.got = r
	if s.res != nil || s.err != nil {
		return s.res, s.err
	}
	return &config.RIExchangeDirectResolveResult{ID: r.ID, Status: r.ExpectedStatus, ResolvedStatus: r.Outcome,
		ResolvedAt: drResolvedAt, Note: r.Note}, nil
}

// drStore holds a stuck row in drRegion for account A, and a second row of
// account B whose id must never appear in a response for the first.
func drStore(rows ...config.RIExchangeDirectRow) *directResolveStore {
	if len(rows) == 0 {
		rows = []config.RIExchangeDirectRow{dlRow(dlID1, dlPtr(dlAcctA), drRegion, "unknown", 1)}
	}
	s := &directResolveStore{directListStore: dlStore(), rows: map[string]config.RIExchangeDirectRow{}}
	for _, r := range rows {
		s.rows[r.ID] = r
	}
	if _, ok := s.rows[drOtherID]; !ok {
		s.rows[drOtherID] = dlRow(drOtherID, dlPtr(dlAcctB), drRegion, "unknown", 2)
	}
	return s
}

// drBody is a valid request for an unknown row in drRegion.
func drBody(outcome, extra string) string {
	return fmt.Sprintf(`{"outcome":%q,"expected_status":"unknown","reason":"source RIs still active in the EC2 console"%s,`+
		`"cloud_check":{"checked_region":%q,"checked_at":%q}}`,
		outcome, extra, drRegion, riResolveAt.Format(time.RFC3339))
}

var drFailedBody = drBody("failed", `,"acknowledge_unknown_outcome":true`)

type drCaller struct {
	perms    []auth.Permission
	accounts []string // nil means unrestricted
	adminKey bool
	email    string
}

func (c drCaller) call(store *directResolveStore, id, body string) (any, error) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID, Email: c.email}, nil).Maybe()
	mockAuth.grantPermissionsScoped(c.perms, c.accounts)
	h := &Handler{config: store, auth: mockAuth,
		// The deployment's account is a third one: only the ROW's account may
		// decide authorization.
		reshapeAccountResolver: func(context.Context) (string, error) { return "33333333-3333-4333-8333-333333333333", nil }}
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}, Body: body}
	if c.adminKey {
		h.apiKey = apgAdminKy
		req.Headers = map[string]string{"x-api-key": apgAdminKy}
	}
	return h.resolveRIExchangeDirectExecution(context.Background(), req, id)
}

func TestResolveRIExchangeDirect_PassesOperatorDecisionToStore(t *testing.T) {
	logs := captureLogs(t)
	store := drStore()
	out, err := drCaller{perms: riExecute, email: "ops@example.com"}.call(store, dlID1, drFailedBody)
	require.NoError(t, err)
	require.Equal(t, 1, store.called)
	got := store.got
	assert.Equal(t, dlID1, got.ID)
	assert.Equal(t, config.RIExchangeDirectStatusFailed, got.Outcome)
	assert.Equal(t, config.RIExchangeDirectStatusUnknown, got.ExpectedStatus)
	assert.Equal(t, riResolveAt, got.CheckedAt)
	require.NotNil(t, got.ResolvedByUserID)
	assert.Equal(t, apgUserID, *got.ResolvedByUserID)
	assert.Equal(t, "ops@example.com", got.ResolvedBy)
	assert.Equal(t, `operator:failed: region="eu-west-1" checked_at=2026-10-19T08:00:00Z ack=true reason="source RIs still active in the EC2 console"`, got.Note)
	resp, ok := out.(riExchangeDirectResolveResponse)
	require.True(t, ok)
	assert.Equal(t, riExchangeDirectResolveResponse{ID: dlID1, Status: "unknown", ResolvedStatus: "failed",
		ResolvedAt: drResolvedAt, Note: got.Note}, resp)
	for _, want := range []string{dlID1, apgUserID, "outcome=failed", "via="} {
		assert.Contains(t, logs.String(), want)
	}

	completed := drStore()
	_, err = drCaller{perms: riExecute}.call(completed, dlID1, drBody("completed", ""))
	require.NoError(t, err)
	assert.Equal(t, `operator:completed: region="eu-west-1" checked_at=2026-10-19T08:00:00Z reason="source RIs still active in the EC2 console"`, completed.got.Note)
}

// The store replays only a byte-identical note and refuses any other as a
// conflict, so two checks a fraction of a second apart must not share a note.
func TestResolveRIExchangeDirect_SubSecondCheckedAtChangesNote(t *testing.T) {
	body := func(at string) string {
		return fmt.Sprintf(`{"outcome":"completed","expected_status":"unknown","reason":"source RIs still active in the EC2 console",`+
			`"cloud_check":{"checked_region":%q,"checked_at":%q}}`, drRegion, at)
	}
	first, second := drStore(), drStore()
	_, err := drCaller{perms: riExecute}.call(first, dlID1, body("2026-10-19T08:00:00.1Z"))
	require.NoError(t, err)
	_, err = drCaller{perms: riExecute}.call(second, dlID1, body("2026-10-19T08:00:00.2Z"))
	require.NoError(t, err)
	assert.NotEqual(t, first.got.Note, second.got.Note, "equal notes would replay instead of conflicting")
	assert.Contains(t, first.got.Note, "checked_at=2026-10-19T08:00:00.1Z")
}

func TestResolveRIExchangeDirect_ReplayIsReportedAndNotLogged(t *testing.T) {
	logs := captureLogs(t)
	store := drStore()
	store.res = &config.RIExchangeDirectResolveResult{ID: dlID1, Status: "unknown", ResolvedStatus: "failed",
		ResolvedAt: drResolvedAt, Note: "n", Replayed: true}
	out, err := drCaller{perms: riExecute}.call(store, dlID1, drFailedBody)
	require.NoError(t, err)
	assert.True(t, out.(riExchangeDirectResolveResponse).Replayed)
	assert.NotContains(t, logs.String(), "resolved by operator", "a replay wrote nothing, so it logs no resolution")
}

func TestResolveRIExchangeDirect_MapsStoreOutcomes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"missing at resolve time", config.ErrRIExchangeResolveNotFound, 404},
		{"checked_at outside the window", fmt.Errorf("%w: checked_at is in the future", config.ErrRIExchangeResolveInvalid), 400},
		{"already settled", fmt.Errorf("%w: the exchange is already completed", config.ErrRIExchangeResolveConflict), 409},
		{"may still be in flight", fmt.Errorf("%w: the exchange may still be in flight; wait until 2026-11-08T09:10:00Z", config.ErrRIExchangeResolveConflict), 409},
		{"already resolved differently", fmt.Errorf("%w: the exchange was already resolved as failed", config.ErrRIExchangeResolveConflict), 409},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := drStore()
			store.err = tc.err
			_, err := drCaller{perms: riExecute}.call(store, dlID1, drFailedBody)
			if tc.want == 404 {
				require.ErrorIs(t, err, errNotFound)
				return
			}
			assert.Equal(t, tc.want, clientCode(t, err))
		})
	}
	t.Run("an unexpected store failure is not a client error", func(t *testing.T) {
		store := drStore()
		store.err = errors.New("connection reset")
		_, err := drCaller{perms: riExecute}.call(store, dlID1, drFailedBody)
		require.Error(t, err)
		_, isClient := IsClientError(err)
		assert.False(t, isClient)
	})
}

func TestResolveRIExchangeDirect_Needs503WithoutTheStore(t *testing.T) {
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, "tok").Return(&Session{UserID: apgUserID}, nil).Maybe()
	mockAuth.grantPermissions(riExecute)
	h := &Handler{config: new(MockConfigStore), auth: mockAuth}
	req := &events.LambdaFunctionURLRequest{Headers: map[string]string{"Authorization": "Bearer tok"}, Body: drFailedBody}
	_, err := h.resolveRIExchangeDirectExecution(context.Background(), req, dlID1)
	assert.Equal(t, 503, clientCode(t, err))
}
