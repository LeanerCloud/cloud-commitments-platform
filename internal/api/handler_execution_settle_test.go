package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestDecodeExecutionSettle(t *testing.T) {
	ok, err := decodeExecutionSettle(`{"reason":"  checked the account  ","acknowledge_unknown_outcome":true}`)
	require.NoError(t, err)
	require.Equal(t, "checked the account", ok.Reason)

	for name, body := range map[string]string{
		"empty":         ``,
		"no ack":        `{"reason":"checked the account"}`,
		"ack false":     `{"reason":"checked the account","acknowledge_unknown_outcome":false}`,
		"short":         `{"reason":"  short  ","acknowledge_unknown_outcome":true}`,
		"long":          `{"reason":"` + strings.Repeat("x", executionSettleReasonMax+1) + `","acknowledge_unknown_outcome":true}`,
		"control":       `{"reason":"checked the\naccount","acknowledge_unknown_outcome":true}`,
		"unknown field": `{"reason":"checked the account","acknowledge_unknown_outcome":true,"x":1}`,
		"two bodies":    `{"reason":"checked the account","acknowledge_unknown_outcome":true}{}`,
		"too large":     strings.Repeat(" ", executionSettleBodyMax+1),
	} {
		_, err := decodeExecutionSettle(body)
		var ce *clientError
		require.True(t, errors.As(err, &ce), name)
		require.Equal(t, 400, ce.code, name)
	}
}

func TestMapExecutionSettleError(t *testing.T) {
	require.True(t, IsNotFoundError(mapExecutionSettleError(config.ErrExecutionSettleNotFound)))
	for sentinel, code := range map[error]int{config.ErrExecutionSettleInvalid: 400, config.ErrExecutionSettleConflict: 409} {
		var ce *clientError
		require.True(t, errors.As(mapExecutionSettleError(fmt.Errorf("%w: detail", sentinel)), &ce))
		require.Equal(t, code, ce.code)
	}
	other := errors.New("db down")
	require.Equal(t, other, mapExecutionSettleError(other), "an internal error is not a client error")
}

// settleSpyStore records whether the settle write was reached.
type settleSpyStore struct {
	*MockConfigStore
	settleCalls int
}

func (s *settleSpyStore) SettleFailedExecution(context.Context, config.ExecutionSettleRequest) (*config.ExecutionSettlement, error) {
	s.settleCalls++
	return nil, NewClientError(418, "test sentinel: settle reached")
}

// The settlement is stamped as outcome_settled_by, so the stateless admin key,
// which has no user identity, is refused before anything is read or written.
func TestSettleExecution_AdminAPIKeyRefused(t *testing.T) {
	ctx := context.Background()
	store := settleSpyForAdminKey()
	handler := &Handler{config: store, apiKey: fourEyesAdminKey}

	_, err := handler.settleExecution(ctx, adminKeyRequest(`{"reason":"checked the subscription: nothing was bought","acknowledge_unknown_outcome":true}`), "11111111-1111-4111-8111-111111111111")

	ce, ok := IsClientError(err)
	require.True(t, ok, "expected ClientError, got %T: %v", err, err)
	require.Equal(t, 403, ce.code)
	require.Contains(t, ce.message, "signed-in user or a user API key")
	require.Zero(t, store.settleCalls)
}

// settleSpyForAdminKey answers the execution read too, so a handler that lost
// the admin-key refusal fails the 403 assertion instead of a mock panic.
func settleSpyForAdminKey() *settleSpyStore {
	store := &settleSpyStore{MockConfigStore: new(MockConfigStore)}
	store.MockConfigStore.On("GetExecutionByID", mock.Anything, mock.Anything).
		Return(nil, errors.New("test sentinel: execution read reached")).Maybe()
	return store
}

// The same refusal through the router, so a route wired to another handler
// (or one that skips this check) cannot reintroduce the admin key.
func TestSettleRoute_AdminAPIKeyRefused(t *testing.T) {
	store := settleSpyForAdminKey()
	r := NewRouter(&Handler{config: store, apiKey: fourEyesAdminKey})
	const path = "/api/purchases/11111111-1111-4111-8111-111111111111/settle"
	req := adminKeyRequest(`{"reason":"checked the subscription: nothing was bought","acknowledge_unknown_outcome":true}`)
	req.RequestContext.HTTP.Method = "POST"
	req.RequestContext.HTTP.Path = path

	_, err := r.Route(context.Background(), "POST", path, req)

	ce, ok := IsClientError(err)
	require.True(t, ok, "expected ClientError, got %T: %v", err, err)
	require.Equal(t, 403, ce.code)
	require.Zero(t, store.settleCalls)
}
