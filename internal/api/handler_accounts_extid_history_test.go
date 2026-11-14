package api

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Issue #120 item 3: an external_id change that would orphan history rows is
// refused with a 409 and a stable reason code.
func TestUpdateAccount_ExternalIDChangeWithHistoryIs409(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	setupAdminAuth(ctx, mockAuth)

	existing := sampleAccount()
	store := new(MockConfigStore)
	store.On("GetCloudAccount", ctx, existing.ID).Return(&existing, nil)
	store.On("UpdateCloudAccount", ctx, mock.Anything).
		Return(fmt.Errorf("wrapped: %w", config.ErrAccountExternalIDHasHistory)).Once()
	handler := &Handler{auth: mockAuth, config: store}

	body := `{"name":"Test Account","provider":"aws","external_id":"210987654321"}`
	result, err := handler.updateAccount(ctx, updateAccountRequest(body, existing.ID), existing.ID)

	assert.Nil(t, result)
	require.Error(t, err)
	ce, ok := IsClientError(err)
	require.True(t, ok, "want a client error, got %v", err)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, ce.message, "external ID cannot be changed")
	assert.Equal(t, "external_id_has_history", ce.details["reason"])
	store.AssertExpectations(t)
}
