package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Per-account arming must check the execute:purchases grant's own account and
// provider constraints against the plan or config being armed, not only that
// the caller holds the verb somewhere.

const (
	apsAcctA = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	apsAcctB = "cccccccc-cccc-cccc-cccc-cccccccccccc"
)

func apsGrant(c *auth.PermissionConstraints) []auth.Permission {
	return []auth.Permission{
		{Action: auth.ActionAdmin, Resource: auth.ResourceAll},
		{Action: auth.ActionExecute, Resource: auth.ResourcePurchases, Constraints: c},
	}
}

func apsPlanCall(t *testing.T, perms []auth.Permission, planAccounts []string) (*MockConfigStore, error) {
	t.Helper()
	store := new(MockConfigStore)
	store.On("GetPurchasePlan", mock.Anything, apgPlanID).Return(apgStoredPlan(false, apgCap(1000)), nil)
	accts := make([]config.CloudAccount, 0, len(planAccounts))
	for _, id := range planAccounts {
		accts = append(accts, config.CloudAccount{ID: id, Provider: "aws"})
	}
	store.On("GetPlanAccounts", mock.Anything, apgPlanID).Return(accts, nil).Maybe()
	store.On("UpdatePurchasePlan", mock.Anything, mock.Anything).Return(nil).Maybe()
	h, req := apgCaller{perms: perms}.handlerAndReq(store, `{"auto_purchase":true}`)
	_, err := h.patchPlan(context.Background(), req, apgPlanID)
	return store, err
}

func TestAutoPurchaseScope_PlanArm(t *testing.T) {
	onlyA := &auth.PermissionConstraints{AccountIDs: []string{apsAcctA}}
	for _, tc := range []struct {
		name         string
		constraints  *auth.PermissionConstraints
		planAccounts []string
		wantRefused  bool
	}{
		{"grant scoped to A, plan for A: allowed", onlyA, []string{apsAcctA}, false},
		{"grant scoped to A, plan for B: refused", onlyA, []string{apsAcctB}, true},
		{"grant scoped to A, plan for A and B: refused", onlyA, []string{apsAcctA, apsAcctB}, true},
		{"grant scoped to A, plan with no accounts: refused", onlyA, nil, true},
		{"unconstrained grant, plan for B: allowed", nil, []string{apsAcctB}, false},
		{"unconstrained grant, plan with no accounts: allowed", nil, nil, false},
		{"grant scoped to azure, aws plan: refused", &auth.PermissionConstraints{Providers: []string{"azure"}}, []string{apsAcctA}, true},
		{"grant scoped to aws, aws plan: allowed", &auth.PermissionConstraints{Providers: []string{"aws"}}, []string{apsAcctA}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := apsPlanCall(t, apsGrant(tc.constraints), tc.planAccounts)
			if tc.wantRefused {
				apgRefused(t, err)
				assert.False(t, apgCalled(store, "UpdatePurchasePlan"))
				return
			}
			require.NoError(t, err)
			assert.True(t, apgCalled(store, "UpdatePurchasePlan"))
		})
	}
}

func TestAutoPurchaseScope_PlanCreateArm(t *testing.T) {
	onlyA := apsGrant(&auth.PermissionConstraints{AccountIDs: []string{apsAcctA}})
	for _, tc := range []struct {
		name        string
		account     string
		wantRefused bool
	}{
		{"scoped to A, create for A: allowed", apsAcctA, false},
		{"scoped to A, create for B: refused", apsAcctB, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := new(MockConfigStore)
			store.On("CreatePurchasePlan", mock.Anything, mock.Anything).Return(nil).Maybe()
			store.On("SetPlanAccounts", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
			store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
				return &config.CloudAccount{ID: id, Provider: "aws"}, nil
			}
			body := `{"name":"n","enabled":true,"auto_purchase":true,"max_step_commitment":1000,"provider":"aws","service":"rds","target_accounts":["` + tc.account + `"]}`
			h, req := apgCaller{perms: onlyA}.handlerAndReq(store, body)
			_, err := h.createPlan(context.Background(), req)
			if tc.wantRefused {
				apgRefused(t, err)
				assert.False(t, apgCalled(store, "CreatePurchasePlan"))
				return
			}
			require.NoError(t, err)
			assert.True(t, apgCalled(store, "CreatePurchasePlan"))
		})
	}
}

func TestAutoPurchaseScope_LadderConfigArm(t *testing.T) {
	for _, tc := range []struct {
		name        string
		constraints *auth.PermissionConstraints
		wantRefused bool
	}{
		{"scoped to the config's account: allowed", &auth.PermissionConstraints{AccountIDs: []string{lgAcct}}, false},
		{"scoped to another account: refused", &auth.PermissionConstraints{AccountIDs: []string{apsAcctB}}, true},
		{"scoped to another provider: refused", &auth.PermissionConstraints{Providers: []string{"azure"}}, true},
		{"unconstrained: allowed", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := lgCall(apgCaller{perms: apsGrant(tc.constraints)}, nil, lgBody(true, "auto_approve", "1"))
			if tc.wantRefused {
				apgRefused(t, err)
				assert.False(t, apgCalled(store, "UpsertLadderConfig"))
				return
			}
			require.NoError(t, err)
			assert.True(t, apgCalled(store, "UpsertLadderConfig"))
		})
	}
}

func TestAutoPurchaseScope_LadderResolve(t *testing.T) {
	for _, tc := range []struct {
		name    string
		account string
		want    int // 0 = allowed
	}{
		{"scoped to the tranche's account: allowed", scopedInAccount, 0},
		{"scoped to another account: refused", scopedOutAccount, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			perms := apsGrant(&auth.PermissionConstraints{AccountIDs: []string{tc.account}})
			store := newResolveStore()
			err := resolveCall(t, apgCaller{perms: perms}, store, resolveProvenBody)
			if tc.want == 0 {
				require.NoError(t, err)
				return
			}
			assert.Equal(t, tc.want, clientCode(t, err))
			assert.Zero(t, store.called)
		})
	}
}
