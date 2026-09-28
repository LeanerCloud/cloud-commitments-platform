package api

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Issue #92 regression guards for the per-execution account-scope gate on the
// money-moving execution endpoints. The caller is an admin restricted to
// allowed_accounts=["Production"] who holds every money verb, so every RBAC
// check passes and only the scope gate can stop the mutation.
const (
	scopeTestExecID = "92929292-0000-0000-0000-000000000001"
	scopeTestPlanID = "92929292-0000-0000-0000-000000000002"
)

var (
	scopeTestProd  = config.CloudAccount{ID: "acc-prod", Name: "Production"}
	scopeTestStage = config.CloudAccount{ID: "acc-stage", Name: "Staging"}
)

type scopeTestCase struct {
	name           string
	planAccounts   []config.CloudAccount
	cloudAccountID *string
}

var scopeTestCases = []scopeTestCase{
	// The plan is attributed only to an account outside the caller's scope.
	{name: "plan out of scope", planAccounts: []config.CloudAccount{scopeTestStage}},
	// The plan spans an in-scope and an out-of-scope account, so the plan
	// check passes; the execution itself targets the out-of-scope account.
	{name: "cross-account execution", planAccounts: []config.CloudAccount{scopeTestProd, scopeTestStage}, cloudAccountID: &scopeTestStage.ID},
}

type scopeTestEndpoint struct {
	name   string
	status string
	call   func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error)
	// mutation is the store method the endpoint reaches only once every
	// gate has passed; empty when the mutation goes through the purchase
	// manager instead (purchaseMutation).
	mutation         string
	purchaseMutation string
}

var scopeTestEndpoints = []scopeTestEndpoint{
	{
		name: "approve", status: "pending", purchaseMutation: "ApproveAndExecute",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.approvePurchase(context.Background(), req, scopeTestExecID, "")
		},
	},
	{
		name: "cancel", status: "pending", mutation: "CancelExecutionAtomic",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.cancelPurchase(context.Background(), req, scopeTestExecID, "")
		},
	},
	{
		name: "retry", status: "failed", mutation: "SavePurchaseExecution",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.retryPurchase(context.Background(), req, scopeTestExecID)
		},
	},
	{
		name: "revoke completed via session", status: "completed", mutation: "TransitionExecutionStatus",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.revokeViaEmailToken(context.Background(), req, scopeTestExecID, "")
		},
	},
	{
		name: "revoke scheduled", status: "scheduled", mutation: "CancelScheduledExecutionAtomic",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.revokePurchase(context.Background(), req, scopeTestExecID)
		},
	},
	{
		name: "run now", status: "pending", purchaseMutation: "RunPlannedPurchaseNow",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.runPlannedPurchase(context.Background(), req, scopeTestExecID)
		},
	},
}

func TestExecutionEndpoints_OutOfScopeExecutionRefused(t *testing.T) {
	for _, sc := range scopeTestCases {
		for _, ep := range scopeTestEndpoints {
			t.Run(sc.name+"/"+ep.name, func(t *testing.T) {
				store := new(MockConfigStore)
				mockAuth := new(MockAuthService)
				mockPurchase := new(MockPurchaseManager)

				mockAuth.On("ValidateSession", mock.Anything, "operator-token").Return(&Session{
					UserID: "operator-1", Email: "operator@example.com",
				}, nil)
				mockAuth.On("ValidateCSRFToken", mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
				// admin:* plus the money verbs it deliberately does not cover
				// (adminCarvedOuts), so every RBAC gate passes.
				mockAuth.grantPermissionsScoped([]auth.Permission{
					{Action: auth.ActionAdmin, Resource: auth.ResourceAll},
					{Action: auth.ActionExecute, Resource: auth.ResourcePurchases},
					{Action: auth.ActionApproveAny, Resource: auth.ResourcePurchases},
					{Action: auth.ActionRetryAny, Resource: auth.ResourcePurchases},
				}, []string{scopeTestProd.Name})

				creator := "operator-1"
				store.On("GetExecutionByID", mock.Anything, scopeTestExecID).Return(&config.PurchaseExecution{
					ExecutionID:     scopeTestExecID,
					PlanID:          scopeTestPlanID,
					Status:          ep.status,
					CloudAccountID:  sc.cloudAccountID,
					CreatedByUserID: &creator,
				}, nil)
				store.GetPlanAccountsFn = func(_ context.Context, planID string) ([]config.CloudAccount, error) {
					if planID != scopeTestPlanID {
						return nil, nil
					}
					return sc.planAccounts, nil
				}
				store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
					for _, a := range []config.CloudAccount{scopeTestProd, scopeTestStage} {
						if a.ID == id {
							return &a, nil
						}
					}
					return nil, nil
				}

				h := &Handler{auth: mockAuth, config: store, purchase: mockPurchase}
				req := &events.LambdaFunctionURLRequest{
					Headers: map[string]string{"Authorization": "Bearer operator-token"},
					RequestContext: events.LambdaFunctionURLRequestContext{
						HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "POST"},
					},
				}

				_, err := ep.call(h, req)
				require.Error(t, err)
				if ep.mutation != "" {
					store.AssertNotCalled(t, ep.mutation)
				}
				if ep.purchaseMutation != "" {
					mockPurchase.AssertNotCalled(t, ep.purchaseMutation)
				}
				// The session-authed revoke hands an out-of-scope session to
				// the token branch, which answers 401 without a token; every
				// other endpoint returns the enumeration-safe 404.
				if ep.name == "revoke completed via session" {
					ce, ok := IsClientError(err)
					require.True(t, ok, "expected a client error, got %v", err)
					assert.Equal(t, 401, ce.code)
					return
				}
				assert.True(t, IsNotFoundError(err), "expected 404 not-found, got %v", err)
			})
		}
	}
}

// TestRequireExecutionAccess_InScopeCloudAccountAllowed is the positive
// control for the cross-account case above: the same plan, with the execution
// targeting the in-scope account, passes the gate.
func TestRequireExecutionAccess_InScopeCloudAccountAllowed(t *testing.T) {
	ctx := context.Background()
	store := new(MockConfigStore)
	mockAuth := new(MockAuthService)
	mockAuth.grantScoped(scopeTestProd.Name)
	store.On("GetExecutionByID", ctx, scopeTestExecID).Return(&config.PurchaseExecution{
		ExecutionID: scopeTestExecID, PlanID: scopeTestPlanID, CloudAccountID: &scopeTestProd.ID,
	}, nil)
	store.GetPlanAccountsFn = func(context.Context, string) ([]config.CloudAccount, error) {
		return []config.CloudAccount{scopeTestProd, scopeTestStage}, nil
	}
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		require.Equal(t, scopeTestProd.ID, id)
		return &scopeTestProd, nil
	}

	h := &Handler{auth: mockAuth, config: store}
	require.NoError(t, h.requireExecutionAccess(ctx, &Session{UserID: "operator-1"}, scopeTestExecID))
	store.AssertCalled(t, "GetCloudAccount", ctx, scopeTestProd.ID)
}
