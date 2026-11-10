package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Duplicate plan names on create (issue #1734). The mock store runs the
// handler's visibility predicate against the fixture plan's accounts, so a
// wrong predicate fails these tests rather than being bypassed.

const existingPlanID = "55555555-5555-4555-8555-555555555555"

func nameFixture(accounts ...string) *mocks.PlanNameFixture {
	f := &mocks.PlanNameFixture{ID: existingPlanID, Name: "Weekly EC2"}
	for _, id := range accounts {
		f.Accounts = append(f.Accounts, config.CloudAccount{ID: id, Name: "acct-" + id})
	}
	return f
}

func requireNameTakenResponse(t *testing.T, err error) {
	t.Helper()
	ce, ok := IsClientError(err)
	require.True(t, ok, "expected ClientError, got %T: %v", err, err)
	assert.Equal(t, 409, ce.code)
	assert.Equal(t, `a plan named "Weekly EC2" already exists; open it or choose another name`, ce.message)
	assert.Equal(t, codePlanNameTaken, ce.details["code"])
	assert.Equal(t, existingPlanID, ce.details["existing_plan_id"])
}

func TestCreatePlan_DuplicateNameUnrestrictedCallerGets409(t *testing.T) {
	h, store := scopedHandler(t)
	store.NameTakenFixture = nameFixture(scopedOutAccount)

	result, err := h.createPlan(context.Background(), scopedRequest(createPlanBody(scopedInAccount)))

	assert.Nil(t, result)
	requireNameTakenResponse(t, err)
}

func TestCreatePlan_DuplicateNameWithOverlappingAccountGets409ForScopedCaller(t *testing.T) {
	h, store := scopedHandler(t, scopedInAccount)
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Name: "acct-" + id, Provider: "aws"}, nil
	}
	store.NameTakenFixture = nameFixture(scopedInAccount, scopedOutAccount)

	_, err := h.createPlan(context.Background(), scopedRequest(createPlanBody(scopedInAccount)))

	requireNameTakenResponse(t, err)
}

// A scoped caller is never told about, or blocked by, a plan outside their
// scope: the create succeeds and nothing in the response names the other plan.
func TestCreatePlan_DuplicateNameOutsideScopeIsInvisible(t *testing.T) {
	h, store := scopedHandler(t, scopedInAccount)
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Name: "acct-" + id, Provider: "aws"}, nil
	}
	store.NameTakenFixture = nameFixture(scopedOutAccount)
	store.On("CreatePurchasePlanWithAccounts", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	result, err := h.createPlan(context.Background(), scopedRequest(createPlanBody(scopedInAccount)))

	require.NoError(t, err)
	plan, ok := result.(*config.PurchasePlan)
	require.True(t, ok)
	assert.NotEqual(t, existingPlanID, plan.ID)
}

// A plan with no accounts is invisible to a scoped caller, like requirePlanAccess.
func TestCreatePlan_DuplicateNameWithoutAccountsIsInvisibleToScopedCaller(t *testing.T) {
	h, store := scopedHandler(t, scopedInAccount)
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		return &config.CloudAccount{ID: id, Name: "acct-" + id, Provider: "aws"}, nil
	}
	store.NameTakenFixture = nameFixture()
	store.On("CreatePurchasePlanWithAccounts", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	_, err := h.createPlan(context.Background(), scopedRequest(createPlanBody(scopedInAccount)))

	require.NoError(t, err)
}

// A failure to resolve the caller's scope for the visibility check must fail
// the request. Read as "nothing is visible" it would let a duplicate in.
func TestCreatePlan_ScopeErrorFailsBeforeTheStore(t *testing.T) {
	ctx := context.Background()
	scopeCalls := 0
	probe := func(okCalls int) (*Handler, *MockConfigStore) {
		store := new(MockConfigStore)
		auth := new(MockAuthService)
		// Registered before grantAdmin: testify serves the first match.
		auth.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { scopeCalls++ }).Return([]string(nil), nil).Times(okCalls)
		auth.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { scopeCalls++ }).Return(nil, errors.New("scope boom"))
		auth.On("ValidateSession", mock.Anything, scopedToken).Return(&Session{UserID: scopedUserID}, nil).Maybe()
		auth.grantAdmin()
		store.On("CreatePurchasePlanWithAccounts", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
		return &Handler{config: store, auth: auth}, store
	}

	// Count how many scope lookups a successful create makes.
	h, _ := probe(100)
	_, err := h.createPlan(ctx, scopedRequest(createPlanBody(scopedInAccount)))
	require.NoError(t, err)
	total := scopeCalls
	require.Positive(t, total)

	// Let every lookup but the last one succeed: the last is the visibility scope.
	h, store := probe(total - 1)
	result, err := h.createPlan(ctx, scopedRequest(createPlanBody(scopedInAccount)))

	assert.Nil(t, result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get allowed accounts")
	store.AssertNotCalled(t, "CreatePurchasePlanWithAccounts", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestCreatePlan_ProviderMismatchGets400WithTheStoreMessage(t *testing.T) {
	h, store := scopedHandler(t)
	msg := `account "az" has provider="azure", expected one of [aws]`
	store.On("CreatePurchasePlanWithAccounts", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(fmt.Errorf("%w: %s", config.ErrPlanProviderMismatch, msg))

	_, err := h.createPlan(context.Background(), scopedRequest(createPlanBody(scopedInAccount)))

	ce, ok := IsClientError(err)
	require.True(t, ok, "expected ClientError, got %T: %v", err, err)
	assert.Equal(t, 400, ce.code)
	assert.Equal(t, "plan provider mismatch: "+msg, ce.message)
}
