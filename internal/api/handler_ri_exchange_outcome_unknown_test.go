package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/auth"
	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// #1050: an accept whose response was lost (common.ErrOutcomeUnknown) may have
// exchanged the RIs at AWS, so the row stays processing with its reserved
// ceiling, never failed and retryable. The note and body carry only fixed
// text and the sanitized reason.

const outcomeUnknownLeak = "arn:aws:ec2:us-east-1:123456789012:reserved-instances/ri-leak"

func outcomeUnknownExecutor(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
	return "", nil, fmt.Errorf("%w: accept response lost: %w", common.ErrOutcomeUnknown, errors.New("RequestError: send request failed for "+outcomeUnknownLeak))
}

// expectOutcomeUnknownExecution wires the execution half shared by both
// approve paths and returns a pointer to the note the store receives.
func expectOutcomeUnknownExecution(store *MockConfigStore, id string) *string {
	store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD: 1000, RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	store.On("ReserveApprovedRIExchange", mock.Anything, id, "1000.000000", "500.000000").Return("100.000000", nil)
	var note string
	store.On("NoteRIExchangeOutcomeUnknown", mock.Anything, id, mock.Anything).Run(func(args mock.Arguments) {
		note = args.String(2)
	}).Return(nil)
	return &note
}

func assertOutcomeUnknown(t *testing.T, store *MockConfigStore, id string, resp any, note string) {
	t.Helper()
	body, ok := resp.(map[string]any)
	require.True(t, ok, "body must be a status map, got %T", resp)
	assert.Equal(t, "outcome_unknown", body["status"])
	message := fmt.Sprint(body["message"])
	for name, text := range map[string]string{"body message": message, "stored note": note} {
		assert.Contains(t, text, "outcome unknown", name)
		assert.Contains(t, text, "/api/ri-exchange/resolve/"+id, "%s must tell the operator how to resolve it", name)
		assert.NotContains(t, text, outcomeUnknownLeak, "%s must not carry raw provider text", name)
	}
	store.AssertNotCalled(t, "FailRIExchange", mock.Anything, mock.Anything, mock.Anything)
	store.AssertNotCalled(t, "CompleteRIExchangeWithPayment", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func outcomeUnknownRecord(id string) *config.RIExchangeRecord {
	return &config.RIExchangeRecord{ID: id, Status: "processing", Region: "us-east-1", SourceRIIDs: []string{"ri-1"},
		TargetOfferingID: "o", TargetCount: 1, PaymentDue: "90.00"}
}

// [fail-first] The email-token path.
func TestApproveRIExchange_TokenPath_OutcomeUnknownStaysProcessing(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000010501"
	const token = "outcome-unknown-token"
	store := new(MockConfigStore)
	pending := outcomeUnknownRecord(id)
	pending.Status, pending.ApprovalToken = "pending", config.HashApprovalToken(token)
	store.On("GetRIExchangeRecord", mock.Anything, id).Return(pending, nil)
	store.On("TransitionRIExchangeStatus", mock.Anything, id, "pending", "processing", mock.Anything).Return(outcomeUnknownRecord(id), nil)
	note := expectOutcomeUnknownExecution(store, id)
	h := &Handler{config: store, executeExchangeFn: outcomeUnknownExecutor}

	resp, err := h.approveRIExchange(ctx, &events.LambdaFunctionURLRequest{}, id, token)
	require.NoError(t, err, "an unknown outcome is reported in the body, not as a server error")
	assertOutcomeUnknown(t, store, id, resp, *note)
}

// [fail-first] The session path, which also stamps approved_by.
func TestApproveRIExchange_SessionPath_OutcomeUnknownStaysProcessingAndStampsApprover(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000010502"
	ownerID, approverID := "owner-1050", "approver-1050"
	store := new(MockConfigStore)
	authSvc := new(MockAuthService)
	t.Cleanup(func() { authSvc.AssertExpectations(t) })
	session := &Session{UserID: approverID, Email: "approver@example.com"}
	authSvc.On("ValidateSession", mock.Anything, "approver-bearer").Return(session, nil)
	authSvc.On("HasPermissionAPI", mock.Anything, approverID, auth.ActionApproveAny, auth.ResourcePurchases).Return(true, nil)
	authSvc.On("GetAllowedAccountsAPI", mock.Anything, approverID).Return(nil, nil).Maybe()
	authSvc.On("ValidateCSRFToken", mock.Anything, "approver-bearer", "csrf-1050").Return(nil)
	pending := outcomeUnknownRecord(id)
	pending.Status, pending.ApprovalToken, pending.CreatedByUserID = "pending", "tok", &ownerID
	store.On("GetRIExchangeRecord", mock.Anything, id).Return(pending, nil)
	store.On("TransitionRIExchangeStatus", mock.Anything, id, "pending", "processing", mock.Anything).Return(outcomeUnknownRecord(id), nil)
	note := expectOutcomeUnknownExecution(store, id)
	store.On("StampRIExchangeApprovedBy", mock.Anything, id, session.Email).Return(nil)
	h := &Handler{config: store, auth: authSvc, executeExchangeFn: outcomeUnknownExecutor}

	resp, err := h.approveRIExchange(ctx, &events.LambdaFunctionURLRequest{Headers: map[string]string{
		"authorization": "Bearer approver-bearer", "x-csrf-token": "csrf-1050",
	}}, id, "")
	require.NoError(t, err)
	assertOutcomeUnknown(t, store, id, resp, *note)
	store.AssertCalled(t, "StampRIExchangeApprovedBy", mock.Anything, id, session.Email)
}

// [guard] A failed note write is logged; the outcome is still reported and the
// row is still never failed.
func TestExecuteApprovedExchange_OutcomeUnknownNoteWriteFailureStillReported(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000010503"
	store := new(MockConfigStore)
	store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD: 1000, RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	store.On("ReserveApprovedRIExchange", mock.Anything, id, "1000.000000", "500.000000").Return("100.000000", nil)
	store.On("NoteRIExchangeOutcomeUnknown", mock.Anything, id, mock.Anything).Return(errors.New("db down"))
	h := &Handler{config: store, executeExchangeFn: outcomeUnknownExecutor}

	resp, err := h.executeApprovedExchange(ctx, id, outcomeUnknownRecord(id))
	require.NoError(t, err)
	assert.Equal(t, "outcome_unknown", resp.(map[string]any)["status"])
	store.AssertNotCalled(t, "FailRIExchange", mock.Anything, mock.Anything, mock.Anything)
}

// [guard] A plain failure still fails the row and stays retryable.
func TestExecuteApprovedExchange_PlainFailureStillFails(t *testing.T) {
	ctx := context.Background()
	const id = "550e8400-e29b-41d4-a716-000000010504"
	store := new(MockConfigStore)
	store.On("GetGlobalConfig", mock.Anything).Return(&config.GlobalConfig{
		RIExchangeMaxDailyUSD: 1000, RIExchangeMaxPerExchangeUSD: 500,
	}, nil)
	store.On("ReserveApprovedRIExchange", mock.Anything, id, "1000.000000", "500.000000").Return("100.000000", nil)
	store.On("FailRIExchange", mock.Anything, id, mock.Anything).Return(nil)
	h := &Handler{config: store, executeExchangeFn: func(context.Context, exchange.ExchangeExecuteRequest) (string, *exchange.ExchangeQuoteSummary, error) {
		return "", nil, errors.New("InvalidParameterValue: target offering unavailable")
	}}

	resp, err := h.executeApprovedExchange(ctx, id, outcomeUnknownRecord(id))
	require.NoError(t, err)
	assert.Equal(t, "failed", resp.(map[string]any)["status"])
	store.AssertNotCalled(t, "NoteRIExchangeOutcomeUnknown", mock.Anything, mock.Anything, mock.Anything)
}
