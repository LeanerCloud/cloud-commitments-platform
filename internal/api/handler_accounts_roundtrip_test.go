package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Round-trip tests for issue #1907 (pairs A1-A3): every request field reaches
// the store call, not only the converter's output.

const roundTripAccountBody = `{
  "name": "Acme",
  "description": "desc",
  "contact_email": "ops@example.com",
  "provider": "aws",
  "external_id": "123456789012",
  "enabled": false,
  "aws_auth_mode": "role_arn",
  "aws_role_arn": "arn:aws:iam::123456789012:role/cudly",
  "aws_external_id": "0123456789abcdef0123",
  "aws_bastion_id": "bastion-1",
  "aws_web_identity_token_file": "/var/run/secrets/eks.amazonaws.com/serviceaccount/token",
  "aws_is_org_root": true,
  "azure_subscription_id": "sub-1",
  "azure_tenant_id": "tenant-1",
  "azure_client_id": "client-1",
  "azure_auth_mode": "managed_identity",
  "gcp_project_id": "proj-1",
  "gcp_client_email": "sa@proj-1.iam.gserviceaccount.com",
  "gcp_auth_mode": "application_default",
  "gcp_wif_audience": "//iam.googleapis.com/pool"
}`

func roundTripAccountWant() config.CloudAccount {
	return config.CloudAccount{
		Name:                    "Acme",
		Description:             "desc",
		ContactEmail:            "ops@example.com",
		Provider:                "aws",
		ExternalID:              "123456789012",
		Enabled:                 false,
		AWSAuthMode:             "role_arn",
		AWSRoleARN:              "arn:aws:iam::123456789012:role/cudly",
		AWSExternalID:           "0123456789abcdef0123",
		AWSBastionID:            "bastion-1",
		AWSWebIdentityTokenFile: "/var/run/secrets/eks.amazonaws.com/serviceaccount/token",
		AWSIsOrgRoot:            true,
		AzureSubscriptionID:     "sub-1",
		AzureTenantID:           "tenant-1",
		AzureClientID:           "client-1",
		AzureAuthMode:           "managed_identity",
		GCPProjectID:            "proj-1",
		GCPClientEmail:          "sa@proj-1.iam.gserviceaccount.com",
		GCPAuthMode:             "application_default",
		GCPWIFAudience:          "//iam.googleapis.com/pool",
	}
}

func captureAccountSave(store *MockConfigStore, method string) **config.CloudAccount {
	var saved *config.CloudAccount
	store.On(method, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		saved = args.Get(1).(*config.CloudAccount)
	}).Return(nil).Once()
	return &saved
}

func TestCreateAccount_RoundTripEveryField(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	setupAdminAuth(ctx, mockAuth)
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	saved := captureAccountSave(store, "CreateCloudAccount")
	handler := &Handler{auth: mockAuth, config: store}

	_, err := handler.createAccount(ctx, adminRequest(roundTripAccountBody))
	require.NoError(t, err)

	require.NotNil(t, *saved)
	got := **saved
	assert.NotEmpty(t, got.ID)
	assert.False(t, got.CreatedAt.IsZero())
	assert.False(t, got.UpdatedAt.IsZero())
	want := roundTripAccountWant()
	want.ID, want.CreatedAt, want.UpdatedAt = got.ID, got.CreatedAt, got.UpdatedAt
	want.Notices = got.Notices // derived, not a request field
	assert.Equal(t, want, got)
}

func TestUpdateAccount_RoundTripEveryField(t *testing.T) {
	ctx := context.Background()
	mockAuth := new(MockAuthService)
	t.Cleanup(func() { mockAuth.AssertExpectations(t) })
	setupAdminAuth(ctx, mockAuth)
	existing := sampleAccount()
	existing.Enabled = true
	existing.CreatedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	existing.CreatedBy = "creator-1"
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	store.On("GetCloudAccount", ctx, existing.ID).Return(&existing, nil)
	saved := captureAccountSave(store, "UpdateCloudAccount")
	handler := &Handler{auth: mockAuth, config: store}

	_, err := handler.updateAccount(ctx, updateAccountRequest(roundTripAccountBody, existing.ID), existing.ID)
	require.NoError(t, err)

	require.NotNil(t, *saved)
	got := **saved
	assert.False(t, got.UpdatedAt.IsZero())
	want := roundTripAccountWant() // explicit enabled=false and aws_is_org_root=true win over the stored row
	want.ID, want.CreatedAt, want.CreatedBy = existing.ID, existing.CreatedAt, "creator-1"
	want.UpdatedAt = got.UpdatedAt
	want.Notices = got.Notices
	assert.Equal(t, want, got)
}

func TestCreateSelfAccount_RoundTripEveryField(t *testing.T) {
	selfAWSID := strings.Repeat("4", 12)
	cases := []struct {
		name string
		si   *sourceIdentity
		want config.CloudAccount
	}{
		{"aws", &sourceIdentity{Provider: "aws", AccountID: selfAWSID}, config.CloudAccount{
			Provider: "aws", ExternalID: selfAWSID, AWSAuthMode: "role_arn"}},
		{"azure", &sourceIdentity{Provider: "azure", SubscriptionID: "sub-1", TenantID: "tenant-1", ClientID: "client-1"},
			config.CloudAccount{Provider: "azure", ExternalID: "sub-1", AzureAuthMode: "managed_identity",
				AzureSubscriptionID: "sub-1", AzureTenantID: "tenant-1", AzureClientID: "client-1"}},
		{"gcp", &sourceIdentity{Provider: "gcp", ProjectID: "proj-1"}, config.CloudAccount{
			Provider: "gcp", ExternalID: "proj-1", GCPAuthMode: "application_default", GCPProjectID: "proj-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			mockAuth := new(MockAuthService)
			t.Cleanup(func() { mockAuth.AssertExpectations(t) })
			setupAdminAuth(ctx, mockAuth)
			store := new(MockConfigStore)
			t.Cleanup(func() { store.AssertExpectations(t) })
			saved := captureAccountSave(store, "CreateCloudAccount")
			handler := &Handler{auth: mockAuth, config: store}
			mockSourceIdentity(handler, tc.si)

			_, err := handler.createSelfAccount(ctx, adminRequest(""))
			require.NoError(t, err)

			require.NotNil(t, *saved)
			got := **saved
			assert.NotEmpty(t, got.ID)
			want := tc.want
			want.Name, want.Enabled = "CUDly host", true
			want.ID, want.CreatedAt, want.UpdatedAt = got.ID, got.CreatedAt, got.UpdatedAt
			want.IsSelf = got.IsSelf // set after the save on the shared pointer
			want.Notices = got.Notices
			assert.Equal(t, want, got)
		})
	}
}
