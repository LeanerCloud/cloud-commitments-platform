package auth

// Membership grant-ceiling tests (issue #226): checkMembershipGrantCeiling
// applies the same ceiling to group-MEMBERSHIP writes that checkGrantCeiling
// (group_ceiling_permissions_test.go) applies to group-PERMISSION writes.
//
// Fixtures (ceilingActorID, ceilingActorGroupID, stubActorPermissions,
// newCeilingService) are shared with the permission-ceiling tests; see
// group_ceiling_fixtures_test.go.
//
// Every refusal case stubs NO CreateUser / UpdateUser expectation on the
// mock store, so a removed guard fails the test loudly (testify panics on an
// un-stubbed call) rather than passing silently.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var updateUsersOnly = []Permission{{Action: ActionUpdate, Resource: ResourceUsers}}

// Vector (a) from issue #226: an admin who by policy cannot add themself to
// Purchaser instead CREATES a second user directly into Purchaser -- a
// puppet account. Before the fix, CreateUser never saw an actor at all, so
// nothing stopped this.
func TestMembershipCeiling_CreateUser_AdminCannotPuppetIntoPurchaser(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	stubActorPermissions(ctx, mockStore, adminOnly)
	mockStore.On("GetUserByEmail", ctx, "puppet@example.com").Return(nil, nil)
	mockStore.On("GetGroup", ctx, DefaultPurchaserGroupID).Return(&Group{
		ID:   DefaultPurchaserGroupID,
		Name: GroupPurchaser,
		Permissions: []Permission{
			{Action: ActionExecute, Resource: ResourcePurchases},
		},
	}, nil)

	_, err := svc.CreateUser(ctx, ceilingActorID, CreateUserRequest{
		Email:    "puppet@example.com",
		Password: "Sup3rSecretP@ssw0rd!",
		GroupIDs: []string{DefaultPurchaserGroupID},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermissionNotGrantable)
	assert.Contains(t, err.Error(), ActionExecute+":"+ResourcePurchases)
	mockStore.AssertNotCalled(t, "CreateUser", mock.Anything, mock.Anything)
}

// Vector (b) from issue #226: a non-admin custom group holding only
// update:users can move any user into Administrators. Before the fix,
// guardGroupChange returned nil unconditionally for a non-self edit.
func TestMembershipCeiling_UpdateUser_UpdateUsersOnlyCannotPromoteToAdmin(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	stubActorPermissions(ctx, mockStore, updateUsersOnly)
	mockStore.On("GetUserByID", ctx, ceilingTargetID).
		Return(&User{ID: ceilingTargetID, Active: true, GroupIDs: nil}, nil)
	mockStore.On("GetGroup", ctx, DefaultAdminGroupID).Return(&Group{
		ID:          DefaultAdminGroupID,
		Name:        "Administrators",
		Permissions: []Permission{{Action: ActionAdmin, Resource: ResourceAll}},
	}, nil)

	_, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{
		GroupIDs: []string{DefaultAdminGroupID},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermissionCeiling)
	assert.Contains(t, err.Error(), ActionAdmin+":"+ResourceAll)
	mockStore.AssertNotCalled(t, "UpdateUser", mock.Anything, mock.Anything)
}

// Negative control: an actor may still create a user in a group whose
// permissions their own effective set already covers.
func TestMembershipCeiling_CreateUser_AllowedWithinCeiling(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	ordinary := &Group{
		ID:          "44444444-4444-4444-8444-000000000001",
		Name:        "Viewers",
		Permissions: []Permission{{Action: ActionView, Resource: ResourcePlans}},
	}
	stubActorPermissions(ctx, mockStore, adminOnly)
	mockStore.On("GetUserByEmail", ctx, "viewer@example.com").Return(nil, nil)
	mockStore.On("GetGroup", ctx, ordinary.ID).Return(ordinary, nil)
	mockStore.On("CreateUser", ctx, mock.AnythingOfType("*auth.User")).Return(nil).Once()

	_, err := svc.CreateUser(ctx, ceilingActorID, CreateUserRequest{
		Email:    "viewer@example.com",
		Password: "Sup3rSecretP@ssw0rd!",
		GroupIDs: []string{ordinary.ID},
	})
	require.NoError(t, err)
}

// Trusted internal callers (actorUserID == "") bypass the ceiling entirely,
// so bootstrap/seeding paths are unaffected.
func TestMembershipCeiling_CreateUser_InternalCallerUnaffected(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	mockStore.On("GetUserByEmail", ctx, "seed@example.com").Return(nil, nil)
	mockStore.On("CreateUser", ctx, mock.AnythingOfType("*auth.User")).Return(nil).Once()

	_, err := svc.CreateUser(ctx, "", CreateUserRequest{
		Email:    "seed@example.com",
		Password: "Sup3rSecretP@ssw0rd!",
		GroupIDs: []string{DefaultPurchaserGroupID},
	})
	require.NoError(t, err)
}
