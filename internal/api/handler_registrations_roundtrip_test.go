package api

import (
	"context"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-platform/internal/config"
)

// Round-trip test for issue #1907 (pair R1): every RegistrationRequest field,
// the credential pair included, reaches CreateAccountRegistration.
func TestSubmitRegistration_RoundTripEveryField(t *testing.T) {
	const payload = `{"access_key_id":"AKIAEXAMPLE","secret_access_key":"placeholder"}`
	body := mustJSON(t, map[string]any{
		"provider":              "aws",
		"external_id":           "ext-1",
		"account_name":          "Prod",
		"contact_email":         "owner@example.com",
		"description":           "desc",
		"source_provider":       "src-1",
		"aws_role_arn":          "arn:aws:iam::210987654321:role/cudly",
		"aws_auth_mode":         "role_arn",
		"aws_external_id":       "ext-id-1",
		"azure_subscription_id": "sub-1",
		"azure_tenant_id":       "tenant-1",
		"azure_client_id":       "client-1",
		"azure_auth_mode":       "managed_identity",
		"gcp_project_id":        "proj-1",
		"gcp_client_email":      "sa@proj-1.iam.gserviceaccount.com",
		"gcp_auth_mode":         "application_default",
		"gcp_wif_audience":      "//iam.googleapis.com/pool",
		"credential_type":       "aws_access_keys",
		"credential_payload":    payload,
	})
	ctx := context.Background()
	store := new(MockConfigStore)
	t.Cleanup(func() { store.AssertExpectations(t) })
	var stored *config.AccountRegistration
	store.On("CreateAccountRegistration", ctx, mock.Anything).Run(func(args mock.Arguments) {
		stored = args.Get(1).(*config.AccountRegistration)
	}).Return(nil).Once()
	h := &Handler{config: store, credStore: &MockCredentialStore{}}

	resp, err := h.submitRegistration(ctx, &events.LambdaFunctionURLRequest{Body: body})
	require.NoError(t, err)
	require.NotNil(t, stored)

	// MockCredentialStore "encrypts" to the plaintext.
	want := config.AccountRegistration{
		ReferenceToken:       stored.ReferenceToken,
		Status:               "pending",
		Provider:             "aws",
		ExternalID:           "ext-1",
		AccountName:          "Prod",
		ContactEmail:         "owner@example.com",
		Description:          "desc",
		SourceProvider:       "src-1",
		AWSRoleARN:           "arn:aws:iam::210987654321:role/cudly",
		AWSAuthMode:          "role_arn",
		AWSExternalID:        "ext-id-1",
		AzureSubscriptionID:  "sub-1",
		AzureTenantID:        "tenant-1",
		AzureClientID:        "client-1",
		AzureAuthMode:        "managed_identity",
		GCPProjectID:         "proj-1",
		GCPClientEmail:       "sa@proj-1.iam.gserviceaccount.com",
		GCPAuthMode:          "application_default",
		GCPWIFAudience:       "//iam.googleapis.com/pool",
		RegCredentialType:    "aws_access_keys",
		RegCredentialPayload: stored.RegCredentialPayload,
	}
	assert.NotEmpty(t, stored.ReferenceToken)
	assert.JSONEq(t, payload, stored.RegCredentialPayload)
	assert.Equal(t, want, *stored)
	assert.Equal(t, map[string]string{"reference_token": stored.ReferenceToken, "status": "pending"}, resp)
}
