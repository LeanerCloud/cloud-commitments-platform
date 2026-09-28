package api

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Issue #92 regression guards for the per-execution account-scope gate on the
// execution endpoints. The caller is an admin restricted by allowed_accounts
// who holds every money verb, so every RBAC check passes and only the scope
// gate can stop the action.
const (
	scopeTestExecID = "92929292-0000-0000-0000-000000000001"
	scopeTestPlanID = "92929292-0000-0000-0000-000000000002"
)

var errScopeTestReached = errors.New("mutation reached")

var (
	scopeTestProd  = config.CloudAccount{ID: "acc-prod", Name: "Production", ContactEmail: "prod-owner@example.com"}
	scopeTestStage = config.CloudAccount{ID: "acc-stage", Name: "Staging", ContactEmail: "operator@example.com"}
)

// scopeTestKind is one shape of execution, differing in how its buy-in
// accounts are derived (executionAccounts).
type scopeTestKind struct {
	name           string
	planID         string
	cloudAccountID *string
	recs           []config.RecommendationRecord
	// partialScope holds some but not all of the accounts the execution can
	// buy in (or, for the single-account child, the plan's other account).
	partialScope []string
}

// scopeTestRec is a purchasable recommendation, so an allowed call clears
// the post-scope constraint checks and reaches the mutation.
func scopeTestRec(id string, account *string) config.RecommendationRecord {
	return config.RecommendationRecord{
		ID: id, Provider: "aws", Service: "ec2", Region: "us-east-1",
		Count: 1, Term: 1, Payment: "all-upfront", UpfrontCost: 100, CloudAccountID: account,
	}
}

var scopeTestBothRecs = []config.RecommendationRecord{
	scopeTestRec("rec-prod", &scopeTestProd.ID),
	scopeTestRec("rec-stage", &scopeTestStage.ID),
}

var scopeTestKinds = []scopeTestKind{
	// Plan-level parent: CloudAccountID nil, fans out to every plan account.
	{name: "plan parent", planID: scopeTestPlanID, recs: scopeTestBothRecs, partialScope: []string{scopeTestProd.Name}},
	// Fan-out child of the same plan, buying only in Staging.
	{name: "fan-out child", planID: scopeTestPlanID, cloudAccountID: &scopeTestStage.ID,
		recs: []config.RecommendationRecord{scopeTestRec("rec-stage", &scopeTestStage.ID)}, partialScope: []string{scopeTestProd.Name}},
	// Ad-hoc web purchase: plan_id NULL, one recommendation per account.
	{name: "ad-hoc", recs: scopeTestBothRecs, partialScope: []string{scopeTestProd.Name}},
}

type scopeTestScope struct {
	name  string
	scope func(k scopeTestKind) []string
	allow bool
}

var scopeTestScopes = []scopeTestScope{
	{name: "all in scope", allow: true, scope: func(scopeTestKind) []string { return []string{scopeTestProd.Name, scopeTestStage.Name} }},
	{name: "partial scope", scope: func(k scopeTestKind) []string { return k.partialScope }},
	{name: "no scope", scope: func(scopeTestKind) []string { return []string{"Other"} }},
}

type scopeTestEndpoint struct {
	name   string
	status string
	call   func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error)
	// mutation is the store method the endpoint reaches only once every
	// gate has passed; purchaseMutation is the purchase-manager equivalent.
	// Both empty for read-only endpoints.
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
	{
		name: "pause", status: "pending", mutation: "TransitionExecutionStatus",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.pausePlannedPurchase(context.Background(), req, scopeTestExecID)
		},
	},
	{
		name: "resume", status: "paused", mutation: "TransitionExecutionStatus",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.resumePlannedPurchase(context.Background(), req, scopeTestExecID)
		},
	},
	{
		name: "delete", status: "pending", mutation: "TransitionExecutionStatus",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.deletePlannedPurchase(context.Background(), req, scopeTestExecID)
		},
	},
	{
		name: "details", status: "completed",
		call: func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.getPurchaseDetails(context.Background(), req, scopeTestExecID)
		},
	},
}

// scopeTestExecution builds the execution row for a kind.
func scopeTestExecution(k scopeTestKind, status string) *config.PurchaseExecution {
	creator := "operator-1"
	return &config.PurchaseExecution{
		ExecutionID:     scopeTestExecID,
		PlanID:          k.planID,
		Status:          status,
		CloudAccountID:  k.cloudAccountID,
		Recommendations: k.recs,
		CreatedByUserID: &creator,
	}
}

// newScopeTestHandler wires a handler whose session is scoped to scope and
// whose store serves exec. GetPlanAccounts("") errors the way the UUID
// plan_id column does in Postgres, so a gate that queries it for an ad-hoc
// execution surfaces as a 500 rather than a quiet deny.
func newScopeTestHandler(t *testing.T, exec *config.PurchaseExecution, scope []string) (*Handler, *MockConfigStore, *MockPurchaseManager) {
	t.Helper()
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
	}, scope)

	store.On("GetExecutionByID", mock.Anything, scopeTestExecID).Return(exec, nil)
	store.GetPlanAccountsFn = func(_ context.Context, planID string) ([]config.CloudAccount, error) {
		switch planID {
		case "":
			return nil, errors.New(`invalid input syntax for type uuid: ""`)
		case scopeTestPlanID:
			return []config.CloudAccount{scopeTestProd, scopeTestStage}, nil
		}
		return nil, nil
	}
	store.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
		for _, a := range []config.CloudAccount{scopeTestProd, scopeTestStage} {
			if a.ID == id {
				return &a, nil
			}
		}
		return nil, nil
	}
	// Every mutation answers errScopeTestReached, so an allowed call proves
	// it got past all gates without wiring each endpoint's success path.
	store.On("SavePurchaseExecution", mock.Anything, mock.Anything).Return(errScopeTestReached).Maybe()
	store.On("TransitionExecutionStatus", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil, errScopeTestReached).Maybe()
	store.On("CancelExecutionAtomic", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(false, "", errScopeTestReached).Maybe()
	store.On("CancelScheduledExecutionAtomic", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(false, "", errScopeTestReached).Maybe()
	mockPurchase.On("ApproveAndExecute", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errScopeTestReached).Maybe()
	mockPurchase.On("RunPlannedPurchaseNow", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(errScopeTestReached).Maybe()
	mockPurchase.On("CancelExecution", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()

	return &Handler{auth: mockAuth, config: store, purchase: mockPurchase}, store, mockPurchase
}

func scopeTestRequest() *events.LambdaFunctionURLRequest {
	return &events.LambdaFunctionURLRequest{
		Headers: map[string]string{"Authorization": "Bearer operator-token", "X-CSRF-Token": "csrf"},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: "POST"},
		},
	}
}

// TestExecutionEndpoints_ScopeMatrix requires EVERY account an execution can
// buy in to be in scope: a session holding only some of them (or none) gets
// the enumeration-safe 404 and never reaches the mutation, while a session
// holding all of them passes the gate.
func TestExecutionEndpoints_ScopeMatrix(t *testing.T) {
	for _, k := range scopeTestKinds {
		for _, sc := range scopeTestScopes {
			for _, ep := range scopeTestEndpoints {
				t.Run(k.name+"/"+sc.name+"/"+ep.name, func(t *testing.T) {
					h, store, mockPurchase := newScopeTestHandler(t, scopeTestExecution(k, ep.status), sc.scope(k))

					_, err := ep.call(h, scopeTestRequest())

					if !sc.allow {
						require.Error(t, err)
						assert.True(t, IsNotFoundError(err), "expected 404 not-found, got %v", err)
						if ep.mutation != "" {
							store.AssertNotCalled(t, ep.mutation)
						}
						if ep.purchaseMutation != "" {
							mockPurchase.AssertNotCalled(t, ep.purchaseMutation)
						}
						return
					}
					if ep.mutation == "" && ep.purchaseMutation == "" {
						require.NoError(t, err)
						return
					}
					assert.ErrorContains(t, err, errScopeTestReached.Error())
				})
			}
		}
	}
}

// TestExecutionEndpoints_OutOfScopeNoTokenIs404 guards the token fall-through:
// without a token an out-of-scope session must get the same 404 as a missing
// row, not the token/session branch's status guards (409/410/401), which
// would confirm the execution exists and leak its status.
func TestExecutionEndpoints_OutOfScopeNoTokenIs404(t *testing.T) {
	adhoc := scopeTestKinds[2]
	for _, tc := range []struct {
		name   string
		status string
		call   func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error)
	}{
		{"approve wrong status", "completed", func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.approvePurchase(context.Background(), req, scopeTestExecID, "")
		}},
		{"cancel wrong status", "completed", func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.cancelPurchase(context.Background(), req, scopeTestExecID, "")
		}},
		{"revoke", "completed", func(h *Handler, req *events.LambdaFunctionURLRequest) (any, error) {
			return h.revokeViaEmailToken(context.Background(), req, scopeTestExecID, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newScopeTestHandler(t, scopeTestExecution(adhoc, tc.status), []string{"Other"})
			_, err := tc.call(h, scopeTestRequest())
			assert.True(t, IsNotFoundError(err), "expected 404 not-found, got %v", err)
		})
	}
}

// TestCancel_OutOfScopeSessionWithTokenUsesContactGate is the positive
// control for the fall-through: a session outside the account scope that
// carries the email token reaches the token branch, and succeeds because the
// session email is the account's contact_email.
func TestCancel_OutOfScopeSessionWithTokenUsesContactGate(t *testing.T) {
	exec := scopeTestExecution(scopeTestKind{recs: []config.RecommendationRecord{
		scopeTestRec("rec-stage", &scopeTestStage.ID),
	}}, "pending")
	h, _, mockPurchase := newScopeTestHandler(t, exec, []string{scopeTestProd.Name})

	res, err := h.cancelPurchase(context.Background(), scopeTestRequest(), scopeTestExecID, "email-token")

	require.NoError(t, err)
	assert.Equal(t, map[string]string{"status": "canceled"}, res)
	mockPurchase.AssertCalled(t, "CancelExecution", mock.Anything, scopeTestExecID, "email-token", "operator@example.com")
}

// TestExecutionAccounts_AdHocUnattributedDenied: an ad-hoc execution with a
// recommendation lacking a cloud account cannot be attributed, so a scoped
// session is denied even when every attributed account is in scope.
func TestExecutionAccounts_AdHocUnattributedDenied(t *testing.T) {
	empty := ""
	for _, missing := range []*string{nil, &empty} {
		exec := scopeTestExecution(scopeTestKind{recs: []config.RecommendationRecord{
			scopeTestRec("rec-prod", &scopeTestProd.ID),
			scopeTestRec("rec-none", missing),
		}}, "pending")
		h, _, _ := newScopeTestHandler(t, exec, []string{scopeTestProd.Name, scopeTestStage.Name})
		err := h.requireExecutionAccess(context.Background(), &Session{UserID: "operator-1"}, scopeTestExecID)
		assert.True(t, IsNotFoundError(err), "expected 404 not-found, got %v", err)
	}
}

// TestPlannedPurchases_HidesPartiallyScopedParent: the list filters with the
// same rule as the per-row gate, so a plan parent that also buys in an
// out-of-scope account is hidden from a partially-scoped session.
func TestPlannedPurchases_HidesPartiallyScopedParent(t *testing.T) {
	for _, tc := range []struct {
		scope []string
		want  int
	}{
		{[]string{scopeTestProd.Name}, 0},
		{[]string{scopeTestProd.Name, scopeTestStage.Name}, 1},
	} {
		exec := scopeTestExecution(scopeTestKinds[0], "pending")
		h, store, _ := newScopeTestHandler(t, exec, tc.scope)
		store.On("GetPlannedExecutions", mock.Anything, mock.Anything, mock.Anything).Return([]config.PurchaseExecution{*exec}, nil)
		store.On("ListPurchasePlans", mock.Anything, mock.Anything).Return([]config.PurchasePlan{{ID: scopeTestPlanID, Name: "p"}}, nil)

		resp, err := h.getPlannedPurchases(context.Background(), scopeTestRequest())
		require.NoError(t, err)
		assert.Len(t, resp.Purchases, tc.want, "scope %v", tc.scope)
	}
}
