package api

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Tests for clearing the registration credential payload once the credential
// is stored (approve), when the registration is rejected, and when a run
// finds the credential stored but the payload still set.

const clearRegID = "5a5a5a5a-2001-4001-8001-000000000001"

const clearAccountID = "5a5a5a5a-2001-4001-8001-0000000000aa"

// clearCredStore is a credential store whose save result and contents are set
// per test.
type clearCredStore struct {
	MockCredentialStore
	saveErr error
	has     bool
	saved   int
}

func (c *clearCredStore) SaveCredential(_ context.Context, _, _ string, _ []byte) error {
	c.saved++
	return c.saveErr
}

func (c *clearCredStore) HasCredential(_ context.Context, _, _ string) (bool, error) {
	return c.has, nil
}

func clearHandler(t *testing.T, status string, cloudAccountID *string, creds *clearCredStore) (*Handler, *MockConfigStore) {
	t.Helper()
	store := new(MockConfigStore)
	store.On("GetAccountRegistration", mock.Anything, clearRegID).Return(&config.AccountRegistration{
		ID: clearRegID, Status: status, Provider: "gcp", ExternalID: "test-project",
		RegCredentialType: "gcp_workload_identity_config", RegCredentialPayload: mustJSON(t, gcloudWIFConfig()),
		CloudAccountID: cloudAccountID,
	}, nil)
	store.On("ApproveAccountRegistration", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Maybe()
	store.On("TransitionRegistrationStatus", mock.Anything, mock.Anything, "pending", mock.Anything).Return(nil).Maybe()
	return &Handler{auth: new(MockAuthService), config: store, credStore: creds}, store
}

func TestApproveRegistration_SavedCredentialClearsPayload(t *testing.T) {
	creds := &clearCredStore{}
	h, store := clearHandler(t, "pending", nil, creds)
	store.On("ClearRegistrationCredential", mock.Anything, clearRegID).Return(nil).Once()

	_, err := h.approveRegistration(context.Background(), &events.LambdaFunctionURLRequest{Body: gcpApprovalBody}, clearRegID)
	require.NoError(t, err)
	assert.Equal(t, 1, creds.saved)
	store.AssertExpectations(t)
}

func TestApproveRegistration_FailedSaveKeepsPayloadAndLogsError(t *testing.T) {
	buf := captureLogs(t)
	creds := &clearCredStore{saveErr: errors.New("db down")}
	h, store := clearHandler(t, "pending", nil, creds)

	_, err := h.approveRegistration(context.Background(), &events.LambdaFunctionURLRequest{Body: gcpApprovalBody}, clearRegID)
	require.NoError(t, err)
	store.AssertNotCalled(t, "ClearRegistrationCredential", mock.Anything, mock.Anything)
	assert.Contains(t, buf.String(), "ERROR")
	assert.Contains(t, buf.String(), "failed to store credential")
	assert.NotContains(t, buf.String(), mustJSON(t, gcloudWIFConfig()))
}

func TestReclaim_StoredCredentialClearsPayloadOnApprovedRegistration(t *testing.T) {
	acct := clearAccountID
	creds := &clearCredStore{has: true}
	h, store := clearHandler(t, "approved", &acct, creds)
	store.On("ClearRegistrationCredential", mock.Anything, clearRegID).Return(nil).Twice()

	// The approve attempt is refused with 409 but finishes the clear; a
	// repeat is idempotent.
	for i := 0; i < 2; i++ {
		_, err := h.approveRegistration(context.Background(), &events.LambdaFunctionURLRequest{Body: gcpApprovalBody}, clearRegID)
		ce, ok := IsClientError(err)
		require.True(t, ok)
		assert.Equal(t, 409, ce.code)
	}
	store.AssertExpectations(t)
}

func TestReclaim_NoStoredCredentialKeepsPayload(t *testing.T) {
	acct := clearAccountID
	creds := &clearCredStore{has: false}
	h, store := clearHandler(t, "approved", &acct, creds)

	_, err := h.approveRegistration(context.Background(), &events.LambdaFunctionURLRequest{Body: gcpApprovalBody}, clearRegID)
	require.Error(t, err)
	store.AssertNotCalled(t, "ClearRegistrationCredential", mock.Anything, mock.Anything)
}
