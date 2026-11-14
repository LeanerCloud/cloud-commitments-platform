package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func purchaseHeld(c *PermissionConstraints) []Permission {
	return []Permission{
		{Action: ActionUpdate, Resource: ResourceUsers},
		{Action: ActionExecute, Resource: ResourcePurchases, Constraints: c},
	}
}

var membershipConstraintCases = []struct {
	name   string
	held   *PermissionConstraints
	target *PermissionConstraints
	ok     bool
}{
	{name: "capped holder, uncapped group", held: &PermissionConstraints{MaxPurchaseAmount: 100}, target: nil, ok: false},
	{name: "capped holder, higher cap group", held: &PermissionConstraints{MaxPurchaseAmount: 100}, target: &PermissionConstraints{MaxPurchaseAmount: 500}, ok: false},
	{name: "capped holder, uncapped group with scope", held: &PermissionConstraints{MaxPurchaseAmount: 100}, target: &PermissionConstraints{AccountIDs: []string{"a"}}, ok: false},
	{name: "capped holder, lower cap group", held: &PermissionConstraints{MaxPurchaseAmount: 100}, target: &PermissionConstraints{MaxPurchaseAmount: 50}, ok: true},
	{name: "capped holder, equal cap group", held: &PermissionConstraints{MaxPurchaseAmount: 100}, target: &PermissionConstraints{MaxPurchaseAmount: 100}, ok: true},
	{name: "account holder, unconstrained group", held: &PermissionConstraints{AccountIDs: []string{"a"}}, target: nil, ok: false},
	{name: "account holder, wider accounts group", held: &PermissionConstraints{AccountIDs: []string{"a"}}, target: &PermissionConstraints{AccountIDs: []string{"a", "b"}}, ok: false},
	{name: "account holder, other account group", held: &PermissionConstraints{AccountIDs: []string{"a"}}, target: &PermissionConstraints{AccountIDs: []string{"b"}}, ok: false},
	{name: "account holder, subset group", held: &PermissionConstraints{AccountIDs: []string{"a", "b"}}, target: &PermissionConstraints{AccountIDs: []string{"a"}}, ok: true},
	{name: "services holder, wider services group", held: &PermissionConstraints{Services: []string{"ec2"}}, target: &PermissionConstraints{Services: []string{"ec2", "rds"}}, ok: false},
	{name: "services holder, other services group", held: &PermissionConstraints{Services: []string{"ec2"}}, target: &PermissionConstraints{Services: []string{"other"}}, ok: false},
	{name: "services holder, no services group", held: &PermissionConstraints{Services: []string{"ec2"}}, target: nil, ok: false},
	{name: "services holder, equal services group", held: &PermissionConstraints{Services: []string{"ec2"}}, target: &PermissionConstraints{Services: []string{"ec2"}}, ok: true},
	{name: "services holder, subset services group", held: &PermissionConstraints{Services: []string{"ec2", "rds"}}, target: &PermissionConstraints{Services: []string{"ec2"}}, ok: true},
	{name: "regions holder, wider regions group", held: &PermissionConstraints{Regions: []string{"us-east-1"}}, target: &PermissionConstraints{Regions: []string{"us-east-1", "eu-west-1"}}, ok: false},
	{name: "regions holder, other regions group", held: &PermissionConstraints{Regions: []string{"us-east-1"}}, target: &PermissionConstraints{Regions: []string{"other"}}, ok: false},
	{name: "regions holder, no regions group", held: &PermissionConstraints{Regions: []string{"us-east-1"}}, target: nil, ok: false},
	{name: "regions holder, equal regions group", held: &PermissionConstraints{Regions: []string{"us-east-1"}}, target: &PermissionConstraints{Regions: []string{"us-east-1"}}, ok: true},
	{name: "regions holder, subset regions group", held: &PermissionConstraints{Regions: []string{"us-east-1", "eu-west-1"}}, target: &PermissionConstraints{Regions: []string{"us-east-1"}}, ok: true},
	{name: "unconstrained holder, unconstrained group", held: nil, target: nil, ok: true},
	{name: "unconstrained holder, capped group", held: nil, target: &PermissionConstraints{MaxPurchaseAmount: 10}, ok: true},
}

func targetGroupWith(c *PermissionConstraints) *Group {
	return &Group{
		ID:   ceilingTargetID,
		Name: "Target",
		Permissions: []Permission{
			{Action: ActionExecute, Resource: ResourcePurchases, Constraints: c},
		},
	}
}

func TestMembershipCeiling_CarvedOutHonoursHeldConstraints(t *testing.T) {
	ctx := context.Background()
	for _, tc := range membershipConstraintCases {
		t.Run(tc.name, func(t *testing.T) {
			mockStore := new(MockStore)
			svc := newCeilingService(t, mockStore)
			mockStore.On("GetGroup", ctx, ceilingTargetID).Return(targetGroupWith(tc.target), nil)

			err := svc.checkMembershipGrantCeilingWithPerms(ctx, purchaseHeld(tc.held), []string{ceilingTargetID})
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrPermissionNotGrantable)
		})
	}
}

func TestGuardSelfCarvedOutGrant_HonoursHeldConstraints(t *testing.T) {
	ctx := context.Background()
	for _, tc := range membershipConstraintCases {
		t.Run(tc.name, func(t *testing.T) {
			mockStore := new(MockStore)
			svc := newCeilingService(t, mockStore)
			mockStore.On("GetGroup", ctx, ceilingTargetID).Return(targetGroupWith(tc.target), nil)

			err := svc.guardSelfCarvedOutGrant(ctx, purchaseHeld(tc.held), []string{ceilingTargetID}, nil)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrSelfEscalation)
		})
	}
}

// End to end through UpdateUser: a capped purchaser with update:users cannot
// move another user into an uncapped purchasing group.
func TestMembershipCeiling_UpdateUser_CappedPurchaserCannotAssignUncappedGroup(t *testing.T) {
	ctx := context.Background()
	mockStore := new(MockStore)
	t.Cleanup(func() { mockStore.AssertExpectations(t) })
	svc := newCeilingService(t, mockStore)

	stubActorPermissions(ctx, mockStore, purchaseHeld(&PermissionConstraints{MaxPurchaseAmount: 100}))
	mockStore.On("GetUserByID", ctx, ceilingTargetID).
		Return(&User{ID: ceilingTargetID, Active: true}, nil)
	mockStore.On("GetGroup", ctx, DefaultPurchaserGroupID).Return(&Group{
		ID:          DefaultPurchaserGroupID,
		Name:        GroupPurchaser,
		Permissions: []Permission{{Action: ActionExecute, Resource: ResourcePurchases}},
	}, nil)

	_, err := svc.UpdateUser(ctx, ceilingActorID, ceilingTargetID, UpdateUserRequest{
		GroupIDs: []string{DefaultPurchaserGroupID},
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPermissionNotGrantable)
	mockStore.AssertNotCalled(t, "UpdateUserAdminFields", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// Group create/update ceiling: a carved-out permission already on the group
// may be carried through an edit only at constraints no broader than stored.
func TestGroupCeiling_CarvedOutCarryThroughHonoursConstraints(t *testing.T) {
	ctx := context.Background()
	stored := []Permission{{Action: ActionExecute, Resource: ResourcePurchases,
		Constraints: &PermissionConstraints{MaxPurchaseAmount: 100}}}
	cases := []struct {
		name string
		req  *PermissionConstraints
		ok   bool
	}{
		{name: "same cap", req: &PermissionConstraints{MaxPurchaseAmount: 100}, ok: true},
		{name: "lower cap", req: &PermissionConstraints{MaxPurchaseAmount: 10}, ok: true},
		{name: "higher cap", req: &PermissionConstraints{MaxPurchaseAmount: 1000}, ok: false},
		{name: "cap removed", req: nil, ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockStore := new(MockStore)
			svc := newCeilingService(t, mockStore)
			stubActorPermissions(ctx, mockStore, adminOnly)
			req := []Permission{{Action: ActionExecute, Resource: ResourcePurchases, Constraints: tc.req}}

			err := svc.checkGrantCeiling(ctx, ceilingActorID, req, stored)
			if tc.ok {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrPermissionNotGrantable)
		})
	}
}
