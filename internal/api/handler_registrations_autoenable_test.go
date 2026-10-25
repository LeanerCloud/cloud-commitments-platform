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

func TestAccountHasCredentialFreePath(t *testing.T) {
	cases := []struct {
		acct *config.CloudAccount
		name string
		want bool
	}{
		{
			name: "aws role_arn — federated",
			acct: &config.CloudAccount{Provider: "aws", AWSAuthMode: "role_arn"},
			want: true,
		},
		{
			name: "aws workload_identity_federation",
			acct: &config.CloudAccount{Provider: "aws", AWSAuthMode: "workload_identity_federation"},
			want: true,
		},
		{
			name: "aws access_keys — stored",
			acct: &config.CloudAccount{Provider: "aws", AWSAuthMode: "access_keys"},
			want: false,
		},
		{
			name: "azure managed_identity — ambient",
			acct: &config.CloudAccount{Provider: "azure", AzureAuthMode: "managed_identity"},
			want: true,
		},
		{
			name: "azure workload_identity_federation",
			acct: &config.CloudAccount{Provider: "azure", AzureAuthMode: "workload_identity_federation"},
			want: true,
		},
		{
			name: "azure client_secret — stored",
			acct: &config.CloudAccount{Provider: "azure", AzureAuthMode: "client_secret"},
			want: false,
		},
		{
			name: "gcp application_default — ambient",
			acct: &config.CloudAccount{Provider: "gcp", GCPAuthMode: "application_default"},
			want: true,
		},
		{
			name: "gcp WIF with audience — federated",
			acct: &config.CloudAccount{
				Provider:       "gcp",
				GCPAuthMode:    "workload_identity_federation",
				GCPWIFAudience: "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/x/providers/y",
			},
			want: true,
		},
		{
			name: "gcp WIF without audience — legacy stored JSON path",
			acct: &config.CloudAccount{Provider: "gcp", GCPAuthMode: "workload_identity_federation"},
			want: false,
		},
		{
			name: "gcp service_account_key — stored",
			acct: &config.CloudAccount{Provider: "gcp", GCPAuthMode: "service_account_key"},
			want: false,
		},
		{
			name: "unknown provider",
			acct: &config.CloudAccount{Provider: "oci"},
			want: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := accountHasCredentialFreePath(c.acct); got != c.want {
				t.Errorf("accountHasCredentialFreePath(%+v) = %v, want %v", c.acct, got, c.want)
			}
		})
	}
}

// saveFailingCredStore returns err from SaveCredential.
type saveFailingCredStore struct {
	MockCredentialStore
	err error
}

func (s *saveFailingCredStore) SaveCredential(context.Context, string, string, []byte) error {
	return s.err
}

// approveWithStoredPayload approves a registration that carries an encrypted
// credential and returns the registration the handler passed to
// UpdateAccountRegistration.
func approveWithStoredPayload(t *testing.T, saveErr error) *config.AccountRegistration {
	t.Helper()
	ctx := context.Background()
	store := new(MockConfigStore)
	store.On("GetAccountRegistration", mock.Anything, hostCheckRegID).Return(&config.AccountRegistration{
		ID: hostCheckRegID, Status: "pending", Provider: "aws", ExternalID: probeHostAcct,
		RegCredentialType: "aws_access_keys", RegCredentialPayload: "encrypted-payload",
	}, nil)
	store.On("TransitionRegistrationStatus", mock.Anything, mock.Anything, "pending", mock.Anything).Return(nil)
	var updated config.AccountRegistration
	store.On("UpdateAccountRegistration", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		updated = *args.Get(1).(*config.AccountRegistration)
	}).Return(nil)
	h := hostCheckHandler(store, &probeSTS{}, nil)
	h.credStore = &saveFailingCredStore{err: saveErr}
	req := &events.LambdaFunctionURLRequest{Body: roleARNBody(probeHostAcct, "")}
	_, err := h.approveRegistration(ctx, req, hostCheckRegID)
	require.NoError(t, err)
	store.AssertCalled(t, "UpdateAccountRegistration", mock.Anything, mock.Anything)
	return &updated
}

func TestApproveRegistration_ClearsPayloadAfterSavedCredentials(t *testing.T) {
	updated := approveWithStoredPayload(t, nil)
	assert.Empty(t, updated.RegCredentialPayload)
}

// A failed save must not cost the operator the only copy of the credential.
func TestApproveRegistration_KeepsPayloadWhenSaveFails(t *testing.T) {
	updated := approveWithStoredPayload(t, errors.New("db down"))
	assert.Equal(t, "encrypted-payload", updated.RegCredentialPayload)
}
