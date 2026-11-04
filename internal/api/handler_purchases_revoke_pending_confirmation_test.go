package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Issues #1522 and #1529: a revoke that cannot prove Azure accepted the Return
// answers REVOCATION_PENDING_CONFIRMATION with no azure_returned, and only a
// recorded return_posted_at stamp (or this request's own accepted Return)
// lets a response say azure_returned:true. The tests read the response the
// way a client does, as JSON.

const pendingConfirmationMessage = "A refund for this purchase is being processed. Check back in a few minutes; don't start another revoke."

func resultJSON(t *testing.T, result any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	return body
}

func assertPendingConfirmation(t *testing.T, result any) {
	t.Helper()
	body := resultJSON(t, result)
	assert.Equal(t, "REVOCATION_PENDING_CONFIRMATION", body["code"])
	assert.Equal(t, pendingConfirmationMessage, body["message"])
	assert.EqualValues(t, 300, body["retry_after_seconds"])
	assert.NotContains(t, body, "azure_returned", "an unproven outcome must not say whether Azure returned")
}

func assertReconcilePendingReturned(t *testing.T, result any) {
	t.Helper()
	body := resultJSON(t, result)
	assert.Equal(t, "RECONCILE_PENDING", body["code"])
	assert.Equal(t, true, body["azure_returned"])
	assert.NotContains(t, body, "retry_after_seconds")
}

func inFlightRow(stamp *time.Time) *config.PurchaseHistoryRecord {
	r := armReservationRecord()
	r.RevocationInFlight = true
	r.RevokedAt = nil
	r.ReturnPostedAt = stamp
	return r
}

func upFrontResult(t *testing.T, row *config.PurchaseHistoryRecord) any {
	t.Helper()
	ctx := context.Background()
	mockStore := new(MockConfigStore)
	mockAuth := new(MockAuthService)
	mockAuth.On("ValidateSession", ctx, "tok").Return(revokeAdminSession(), nil)
	mockStore.On("GetExecutionByID", ctx, row.PurchaseID).Return(nil, fmt.Errorf("%w: execution %s", config.ErrNotFound, row.PurchaseID))
	mockStore.On("GetPurchaseHistoryByPurchaseID", ctx, row.PurchaseID).Return(row, nil)
	h := &Handler{config: mockStore, auth: mockAuth}
	result, err := h.revokePurchase(ctx, sessionReq("tok"), row.PurchaseID)
	require.NoError(t, err)
	return result
}
