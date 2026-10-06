package api

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Issue #520: DELETE /api/purchases/planned/{id} cancels an execution and
// then disables its plan. A single-account child clears requireExecutionAccess
// on its own account while the plan spans accounts the caller cannot see, so
// the plan disable needs the every-account check of the plan mutation routes.

const plannedDeleteExecID = "55555555-5555-4555-8555-555555555555"

type plannedDeleteFixture struct {
	h     *Handler
	store *MockConfigStore
}

// newPlannedDeleteFixture wires a scoped session on a child execution for the
// in-scope account, whose plan spans planAccounts. The plan is enabled, so a
// disable shows up as an UpdatePurchasePlan call.
func newPlannedDeleteFixture(t *testing.T, planAccounts []config.CloudAccount, scope ...string) plannedDeleteFixture {
	t.Helper()
	h, store := scopedHandler(t, scope...)
	seedPlanAccountsStore(store, planAccounts)
	store.GetPurchasePlanFn = func(_ context.Context, id string) (*config.PurchasePlan, error) {
		return &config.PurchasePlan{ID: id, Name: "scoped plan", Enabled: true}, nil
	}
	childAccount := scopedInAccount
	exec := &config.PurchaseExecution{
		ExecutionID:    plannedDeleteExecID,
		PlanID:         scopePlanID,
		Status:         "pending",
		CloudAccountID: &childAccount,
	}
	canceled := *exec
	canceled.Status = config.StatusCanceled
	store.On("GetExecutionByID", mock.Anything, plannedDeleteExecID).Return(exec, nil)
	store.On("TransitionExecutionStatus", mock.Anything, plannedDeleteExecID, mock.Anything, config.StatusCanceled, mock.Anything).Return(&canceled, nil).Maybe()
	store.On("UpdatePurchasePlan", mock.Anything, mock.Anything).Return(nil).Maybe()
	return plannedDeleteFixture{h: h, store: store}
}

func (f plannedDeleteFixture) delete(t *testing.T) (any, error) {
	t.Helper()
	return NewRouter(f.h).Route(context.Background(), "DELETE", "/api/purchases/planned/"+plannedDeleteExecID, scopedRequest(""))
}

func (f plannedDeleteFixture) planDisables() int {
	n := 0
	for _, c := range f.store.Calls {
		if c.Method == "UpdatePurchasePlan" {
			n++
		}
	}
	return n
}

func TestRouterDispatch_DeletePlannedPurchase_PartiallyScopedKeepsPlanEnabled(t *testing.T) {
	f := newPlannedDeleteFixture(t, []config.CloudAccount{inScopeAccount(), outOfScopeAccount()}, scopedInAccount)
	store := f.store

	resp, err := f.delete(t)

	require.NoError(t, err)
	assert.Equal(t, &StatusResponse{Status: "canceled"}, resp)
	store.AssertNumberOfCalls(t, "TransitionExecutionStatus", 1)
	assert.Zero(t, f.planDisables(), "a plan spanning an out-of-scope account must stay enabled")
	store.AssertNotCalled(t, "GetPurchasePlan", mock.Anything, mock.Anything)
}

func TestRouterDispatch_DeletePlannedPurchase_FullyScopedDisablesPlan(t *testing.T) {
	f := newPlannedDeleteFixture(t, []config.CloudAccount{inScopeAccount(), outOfScopeAccount()}, scopedInAccount, scopedOutAccount)
	store := f.store

	_, err := f.delete(t)

	require.NoError(t, err)
	store.AssertNumberOfCalls(t, "TransitionExecutionStatus", 1)
	store.AssertCalled(t, "UpdatePurchasePlan", mock.Anything, mock.MatchedBy(func(p *config.PurchasePlan) bool {
		return p.ID == scopePlanID && !p.Enabled
	}))
	assert.Equal(t, 1, f.planDisables())
}

func TestRouterDispatch_DeletePlannedPurchase_UnrestrictedDisablesPlanWithoutScopeLookup(t *testing.T) {
	f := newPlannedDeleteFixture(t, []config.CloudAccount{inScopeAccount(), outOfScopeAccount()})
	store := f.store

	_, err := f.delete(t)

	require.NoError(t, err)
	assert.Equal(t, 1, f.planDisables())
	store.AssertNotCalled(t, "GetPlanAccounts", mock.Anything, mock.Anything)
}

// A plan with no accounts is hidden from scoped callers, so the disable is
// skipped like the partially scoped case.
func TestRouterDispatch_DeletePlannedPurchase_ZeroAccountPlanKeepsPlanEnabled(t *testing.T) {
	f := newPlannedDeleteFixture(t, nil, scopedInAccount)
	store := f.store

	_, err := f.delete(t)

	require.NoError(t, err)
	store.AssertNumberOfCalls(t, "TransitionExecutionStatus", 1)
	assert.Zero(t, f.planDisables())
}

// A store failure while resolving the plan's accounts is not a scope refusal:
// it surfaces and the plan is not disabled.
func TestRouterDispatch_DeletePlannedPurchase_PlanAccountLookupFailureSurfaces(t *testing.T) {
	f := newPlannedDeleteFixture(t, nil, scopedInAccount)
	f.store.GetPlanAccountsFn = func(context.Context, string) ([]config.CloudAccount, error) {
		return nil, errors.New("db down")
	}

	_, err := f.delete(t)

	require.Error(t, err)
	assert.False(t, IsNotFoundError(err))
	assert.Zero(t, f.planDisables())
}

// The execution gate still comes first: a caller who holds none of the
// execution's accounts is refused before anything is canceled or disabled.
func TestRouterDispatch_DeletePlannedPurchase_OutOfScopeExecutionRefusedBeforeAnyWrite(t *testing.T) {
	f := newPlannedDeleteFixture(t, []config.CloudAccount{inScopeAccount(), outOfScopeAccount()}, scopedOutAccount)
	store := f.store

	_, err := f.delete(t)

	require.Error(t, err)
	assert.True(t, IsNotFoundError(err), "got %v", err)
	store.AssertNotCalled(t, "TransitionExecutionStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	assert.Zero(t, f.planDisables())
}

// Permission is checked before scope: a caller without any delete or cancel
// verb gets 403 and no execution or plan lookup happens.
func TestRouterDispatch_DeletePlannedPurchase_PermissionRefusedBeforeScope(t *testing.T) {
	f := newPlannedDeleteFixture(t, []config.CloudAccount{inScopeAccount(), outOfScopeAccount()}, scopedInAccount)
	store := f.store
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", mock.Anything, scopedToken).Return(&Session{UserID: scopedUserID}, nil).Maybe()
	mockAuth.grantPermissionsScoped([]auth.Permission{{Action: auth.ActionView, Resource: auth.ResourcePurchases}}, []string{scopedInAccount})
	f.h.auth = mockAuth

	_, err := f.delete(t)

	require.Error(t, err)
	assert.False(t, IsNotFoundError(err), "expected a permission refusal, got %v", err)
	store.AssertNotCalled(t, "GetExecutionByID", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "GetPlanAccounts", mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "TransitionExecutionStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	assert.Zero(t, f.planDisables())
}
