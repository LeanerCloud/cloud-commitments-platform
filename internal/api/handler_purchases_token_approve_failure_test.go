package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/purchase"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// TestHandleRequest_TokenApprove_ErrorMapping pins #532: token-mode approve
// maps a post-claim failure like session approve (502 with the row's real
// status), a lost claim to 409 and an audit loss to 500, and keeps the
// token errors on their 403/410 mapping.
func TestHandleRequest_TokenApprove_ErrorMapping(t *testing.T) {
	const (
		execID   = "12312312-3123-1231-2312-312312312399"
		token    = "token123"
		approver = "admin@example.com"
	)
	ranRow := func(status string) *config.PurchaseExecution {
		return &config.PurchaseExecution{ExecutionID: execID, Status: status}
	}
	tests := []struct {
		name       string
		row        *config.PurchaseExecution
		err        error
		wantStatus int
		wantExecID bool
		wantRow    string
	}{
		{"provider failure after claim", ranRow("failed"), errors.New("provider rejected the purchase"), 502, true, "failed"},
		{"partially completed", ranRow("partially_completed"), errors.New("1 of 2 purchases failed"), 502, true, "partially_completed"},
		{"lost claim", nil, fmt.Errorf("claim: %w", config.ErrExecutionNotInExpectedStatus), 409, false, ""},
		{"audit loss after the run", ranRow("running"), fmt.Errorf("save: %w", config.ErrAuditLoss), 500, false, ""},
		{"invalid token", nil, purchase.ErrInvalidApprovalToken, 403, false, ""},
		{"expired token", nil, purchase.ErrApprovalTokenExpired, 410, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			accountID := "acct-1"
			mockConfig := new(MockConfigStore)
			exec := &config.PurchaseExecution{
				ExecutionID: execID, ApprovalToken: token, Status: "pending",
				Recommendations: []config.RecommendationRecord{{ID: "r1", CloudAccountID: &accountID}},
			}
			mockConfig.GetCloudAccountFn = func(_ context.Context, id string) (*config.CloudAccount, error) {
				return &config.CloudAccount{ID: id, ContactEmail: approver}, nil
			}
			mockConfig.On("GetExecutionByID", mock.Anything, execID).Return(exec, nil)
			mockConfig.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{NotificationEmail: strPtr(approver)}, nil)
			mockAuth := new(MockAuthService)
			mockAuth.On("GetAllowedAccountsAPI", mock.Anything, mock.Anything).Return(nil, nil).Maybe()
			mockAuth.On("ValidateSession", mock.Anything, "sess-tok").Return(&Session{Email: approver}, nil)
			mockAuth.On("HasPermissionAPI", mock.Anything, "", "approve-any", "purchases").Return(false, nil).Maybe()
			mockAuth.On("HasPermissionAPI", mock.Anything, "", "approve-own", "purchases").Return(false, nil).Maybe()
			mockPurchase := new(MockPurchaseManager)
			mockPurchase.On("ApproveExecution", mock.Anything, execID, token, approver).Return(tt.row, "", tt.err)
			handler := &Handler{purchase: mockPurchase, config: mockConfig, auth: mockAuth}

			req := &events.LambdaFunctionURLRequest{
				QueryStringParameters: map[string]string{"token": token},
				Headers:               map[string]string{"authorization": "Bearer sess-tok"},
				RequestContext: events.LambdaFunctionURLRequestContext{
					HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{
						Method: "POST", Path: "/api/purchases/approve/" + execID,
					},
				},
			}
			resp, err := handler.HandleRequest(ctx, req)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, resp.StatusCode, resp.Body)
			if !tt.wantExecID {
				return
			}
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(resp.Body), &body))
			assert.Equal(t, execID, body["execution_id"])
			assert.Equal(t, tt.wantRow, body["status"])
		})
	}
}
