package api

import (
	"context"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Issue #1803: a failed EC2 or Azure savings-plans row that is a proven-unsent
// first attempt (no_submission at attempt 0) cannot have bought anything, so
// Retry is allowed and links through the CAS that re-checks that proof.

const redriveUnsentExecID = "22222222-3333-4444-5555-666666666609"

func unsentRow(provider, service string) *config.PurchaseExecution {
	row := redriveFailedRow(redriveUnsentExecID, provider, service)
	row.NoSubmission = true
	return row
}

// retryWithLinkMocks runs the real retry handler with the successor insert
// mocked and returns the store so the caller registers the link outcome first.
func retryWithLinkMocks(t *testing.T, failed *config.PurchaseExecution) (*Handler, *MockConfigStore) {
	t.Helper()
	session := &Session{UserID: retryCallerID, Email: "operator@example.com"}
	handler, mockConfig, _ := buildSessionRetryHandler(t, failed, session, false, true)
	mockConfig.On("SavePurchaseExecution", mock.Anything, mock.AnythingOfType("*config.PurchaseExecution")).Return(nil).Maybe()
	return handler, mockConfig
}

func TestRetryOfProvenUnsentRowLinksViaUnsentCAS(t *testing.T) {
	for _, tc := range []struct{ name, provider, service string }{
		{"aws ec2", "aws", "ec2"},
		{"azure savings plans", "azure", "savingsplans"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failed := unsentRow(tc.provider, tc.service)
			handler, store := retryWithLinkMocks(t, failed)
			store.On("LinkUnsentRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(true, nil).Once()

			result, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

			require.NoError(t, err, "a proven-unsent first attempt must be retryable")
			assert.NotEmpty(t, result.(map[string]any)["execution_id"])
			store.AssertNotCalled(t, "LinkRetryExecutionAtomic")
		})
	}
}

func TestRetryOfProvenUnsentRowLosingUnsentLinkIs409(t *testing.T) {
	failed := unsentRow("aws", "ec2")
	handler, store := retryWithLinkMocks(t, failed)
	store.On("LinkUnsentRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(false, nil).Once()

	result, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

	require.Error(t, err)
	assert.Nil(t, result)
	ce, ok := IsClientError(err)
	require.True(t, ok)
	assert.Equal(t, 409, ce.code)
	assert.Contains(t, err.Error(), "already retried or settled")
	store.AssertNotCalled(t, "CreateSuppressionTx")
}

// The proof is per row: a submitted EC2 row and a no_submission successor
// (attempt 1) stay refused, whatever the flag says about later attempts.
func TestRetryOfEC2RowWithoutAttemptZeroProofIsRefused(t *testing.T) {
	submitted := redriveFailedRow(redriveUnsentExecID, "aws", "ec2")
	attempt1 := unsentRow("aws", "ec2")
	attempt1.RetryAttemptN = 1
	for name, failed := range map[string]*config.PurchaseExecution{"submitted": submitted, "no_submission attempt 1": attempt1} {
		t.Run(name, func(t *testing.T) {
			tokens, err := purchasesFiredByRetry(t, failed, sessionRetryReq())

			assert.Empty(t, tokens)
			assertRedriveRefused(t, err, "Check EC2 Reserved Instances in the target account")
		})
	}
}

// Rows that were never refused keep the plain CAS and never touch the new one.
func TestRetryOfRDSRowStillLinksViaPlainCAS(t *testing.T) {
	failed := unsentRow("aws", "rds")
	handler, store := retryWithLinkMocks(t, failed)
	store.On("LinkRetryExecutionAtomic", mock.Anything, mock.Anything, failed.ExecutionID, mock.AnythingOfType("string")).Return(true, nil).Once()

	_, err := handler.retryPurchase(context.Background(), sessionRetryReq(), failed.ExecutionID)

	require.NoError(t, err)
	store.AssertNotCalled(t, "LinkUnsentRetryExecutionAtomic")
}

func TestExecutionToHistoryRow_RetryHintForUnsentRows(t *testing.T) {
	for _, tc := range []struct {
		name       string
		provider   string
		service    string
		unsent     bool
		wantUnsafe bool
	}{
		{"unsent ec2 shows Retry", "aws", "ec2", true, false},
		{"unsent azure savings plans shows Retry", "azure", "savingsplans", true, false},
		{"submitted ec2 is not retryable", "aws", "ec2", false, true},
		{"submitted azure savings plans is not retryable", "azure", "savingsplans", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := redriveFailedRow(redriveUnsentExecID, tc.provider, tc.service)
			exec.Error = "boom"
			exec.NoSubmission = tc.unsent

			row := executionToHistoryRow(*exec, "", "")

			assert.Equal(t, tc.wantUnsafe, row.RedriveUnsafe)
			assert.Equal(t, tc.wantUnsafe, row.OpsHint != "", "ops_hint is what hides the Retry button")
		})
	}
}
