package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// An address is an authorization attribute: password reset mails a reset link
// to it. Changing a user's email therefore hands that user's account to
// whoever controls the new address, so it is held to the same grant ceiling as
// adding the target's groups.

func requireEmailChangeRefused(t *testing.T, err error, sentinel error, grant string, mockStore *MockStore) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Contains(t, err.Error(), grant)
	mockStore.AssertNotCalled(t, "UpdateUserAdminFields", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestEmailChangeCeiling_UpdateUsersOnlyCannotRetargetAdmin(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	stubActorPermissions(ctx, mockStore, updateUsersOnly)
	stubAdminTarget(ctx, mockStore, true)
	mockStore.On("GetUserByEmail", mock.Anything, mock.Anything).Return(nil, nil).Maybe()

	email := "attacker@example.com"
	_, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{Email: &email})

	requireEmailChangeRefused(t, err, ErrPermissionCeiling, ActionAdmin+":"+ResourceAll, mockStore)
}

// Carved-out verbs (#923) are out of reach even for an administrator.
func TestEmailChangeCeiling_AdminCannotRetargetPurchaser(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	stubActorPermissions(ctx, mockStore, adminOnly)
	stubPurchaserTarget(ctx, mockStore, true)
	mockStore.On("GetUserByEmail", mock.Anything, mock.Anything).Return(nil, nil).Maybe()

	email := "attacker@example.com"
	_, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{Email: &email})

	requireEmailChangeRefused(t, err, ErrPermissionNotGrantable, ActionExecute+":"+ResourcePurchases, mockStore)
}

// Negative controls: the ceiling does not block the legitimate cases.
func TestEmailChangeCeiling_WithinCeilingAllowed(t *testing.T) {
	ctx := context.Background()
	email := "new@example.com"

	t.Run("admin retargets admin", func(t *testing.T) {
		mockStore := new(MockStore)
		t.Cleanup(func() { mockStore.AssertExpectations(t) })
		svc := newCeilingService(t, mockStore)
		stubActorPermissions(ctx, mockStore, adminOnly)
		stubAdminTarget(ctx, mockStore, true)
		mockStore.On("GetUserByEmail", ctx, email).Return(nil, nil)
		mockStore.On("UpdateUserAdminFields", ctx, mock.AnythingOfType("*auth.User"), mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

		user, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{Email: &email})
		require.NoError(t, err)
		assert.Equal(t, email, user.Email)
	})

	t.Run("update:users retargets a user with no groups", func(t *testing.T) {
		mockStore := new(MockStore)
		t.Cleanup(func() { mockStore.AssertExpectations(t) })
		svc := newCeilingService(t, mockStore)
		stubActorPermissionsMaybe(ctx, mockStore, updateUsersOnly)
		mockStore.On("GetUserByID", ctx, ceilingTargetID).Return(&User{ID: ceilingTargetID, Active: true}, nil)
		mockStore.On("GetUserByEmail", ctx, email).Return(nil, nil)
		mockStore.On("UpdateUserAdminFields", ctx, mock.AnythingOfType("*auth.User"), mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

		user, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{Email: &email})
		require.NoError(t, err)
		assert.Equal(t, email, user.Email)
	})

	t.Run("an unchanged address is not an email change", func(t *testing.T) {
		mockStore := new(MockStore)
		t.Cleanup(func() { mockStore.AssertExpectations(t) })
		svc := newCeilingService(t, mockStore)
		stubActorPermissionsMaybe(ctx, mockStore, updateUsersOnly)
		stubAdminTarget(ctx, mockStore, true)
		mockStore.On("UpdateUserAdminFields", ctx, mock.AnythingOfType("*auth.User"), mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

		same := ""
		_, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{Email: &same})
		require.NoError(t, err)
	})
}
