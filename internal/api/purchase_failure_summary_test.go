package api

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fakeEncodedBlob = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789LongEncodedBlob"
	fakeAWSDenied   = "operation error EC2: PurchaseReservedInstancesOffering, https response error StatusCode: 403, " +
		"RequestID: 11111111-2222-3333-4444-555555555555, api error UnauthorizedOperation: You are not authorized. " +
		"User: arn:aws:sts::123456789012:assumed-role/cudly-role/session. " +
		"Encoded authorization failure message: " + fakeEncodedBlob
)

var providerTextNeedles = []string{"arn:aws", "123456789012", "Encoded authorization", fakeEncodedBlob, "RequestID"}

func assertNoProviderText(t *testing.T, where, s string) {
	t.Helper()
	for _, n := range providerTextNeedles {
		assert.NotContains(t, s, n, where)
	}
}

// One realistic provider denial must stay out of every user-facing surface.
func TestProviderDenial_NeverReachesUserSurfaces(t *testing.T) {
	const execID = "11111111-1111-1111-1111-111111111111"
	raw := errors.New(fakeAWSDenied)
	rec := config.RecommendationRecord{Provider: "aws", Service: "rds", Error: fakeAWSDenied}

	t.Run("direct execute 502", func(t *testing.T) {
		err := executeFailureError(execID, "executed", &config.PurchaseExecution{Status: "failed"}, raw)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 502, ce.code)
		body, mErr := json.Marshal(ce.Details())
		require.NoError(t, mErr)
		assertNoProviderText(t, "message", ce.Error())
		assertNoProviderText(t, "details", string(body))
		assert.Contains(t, ce.Error(), execID)
		assert.Equal(t, "access_denied", ce.Details()["category"])
	})

	t.Run("history failed row", func(t *testing.T) {
		row := executionToHistoryRow(config.PurchaseExecution{
			ExecutionID: execID, Status: "failed", Error: "IAM denied: " + fakeAWSDenied,
		}, "", "")
		assertNoProviderText(t, "status_description", row.StatusDescription)
		assertNoProviderText(t, "ops_hint", row.OpsHint)
		assert.Equal(t, "Grant the deploy role missing IAM permission, then retry", row.OpsHint,
			"the hint still resolves from the raw text")
	})

	t.Run("purchase details", func(t *testing.T) {
		exec := &config.PurchaseExecution{ExecutionID: execID, Status: "failed", Error: fakeAWSDenied,
			Recommendations: []config.RecommendationRecord{rec}}
		resp := buildPurchaseDetailsResponse(exec, "plan")
		body, err := json.Marshal(resp)
		require.NoError(t, err)
		assertNoProviderText(t, "details body", string(body))
		assert.Equal(t, fakeAWSDenied, exec.Error, "stored row is not mutated")
		assert.Equal(t, fakeAWSDenied, exec.Recommendations[0].Error, "stored rec is not mutated")
	})

	t.Run("retry 409 failure_reason", func(t *testing.T) {
		creator := retryCallerID
		failed := &config.PurchaseExecution{ExecutionID: retryExecID, Status: "failed",
			Error: "AssumeRole error: IAM denied: " + fakeAWSDenied, CreatedByUserID: &creator}
		handler, _, _ := buildSessionRetryHandler(t, failed, &Session{UserID: retryCallerID}, false, true)
		_, err := handler.retryPurchase(t.Context(), sessionRetryReq(), retryExecID)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 409, ce.code)
		body, mErr := json.Marshal(ce.Details())
		require.NoError(t, mErr)
		assertNoProviderText(t, "retry details", string(body)+ce.Error())
		assert.NotEmpty(t, ce.Details()["failure_reason"])
	})
}

func TestExecuteFailureError_PartialAndAuditLossHideProviderText(t *testing.T) {
	for _, status := range []string{"partially_completed", "failed"} {
		err := executeFailureError("e1", "executed", &config.PurchaseExecution{Status: status},
			errors.New("some purchases failed (partial success): "+fakeAWSDenied))
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assertNoProviderText(t, status, ce.Error())
	}
	err := executeFailureError("e1", "executed", &config.PurchaseExecution{Status: "failed"},
		errors.Join(config.ErrAuditLoss, errors.New(fakeAWSDenied)))
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, 500, ce.code)
	assertNoProviderText(t, "audit loss", ce.Error())
}
